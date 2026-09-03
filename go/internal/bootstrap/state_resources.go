package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	platformstate "github.com/nyroway/nyro/go/internal/platform/state"
	"github.com/nyroway/nyro/go/internal/quota"
	quotaredis "github.com/nyroway/nyro/go/internal/quota/redis"
)

const (
	stateConnectTimeout = 3 * time.Second
	stateHealthInterval = 5 * time.Second
)

type stateResource struct {
	config platformstate.Config
	store  quota.Store
	ping   func(context.Context) error
	close  func() error
	refs   int
}

type statePool struct {
	mu      sync.Mutex
	entries map[platformstate.Config]*stateResource
	closed  bool
}

func newStatePool() *statePool {
	return &statePool{entries: make(map[platformstate.Config]*stateResource)}
}

func (pool *statePool) acquire(ctx context.Context, cfg platformstate.Config) (*stateResource, error) {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil, errors.New("state resource pool is closed")
	}
	if existing := pool.entries[cfg]; existing != nil {
		existing.refs++
		pool.mu.Unlock()
		return existing, nil
	}
	pool.mu.Unlock()

	candidate, err := buildStateResource(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		_ = candidate.closeResource()
		return nil, errors.New("state resource pool is closed")
	}
	if existing := pool.entries[cfg]; existing != nil {
		existing.refs++
		pool.mu.Unlock()
		_ = candidate.closeResource()
		return existing, nil
	}
	candidate.refs = 1
	pool.entries[cfg] = candidate
	pool.mu.Unlock()
	return candidate, nil
}

func (pool *statePool) release(resource *stateResource) error {
	if resource == nil {
		return nil
	}
	pool.mu.Lock()
	current := pool.entries[resource.config]
	if current != resource || resource.refs <= 0 {
		pool.mu.Unlock()
		return nil
	}
	resource.refs--
	if resource.refs != 0 {
		pool.mu.Unlock()
		return nil
	}
	delete(pool.entries, resource.config)
	pool.mu.Unlock()
	return resource.closeResource()
}

func (pool *statePool) Close() error {
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	entries := make([]*stateResource, 0, len(pool.entries))
	for _, entry := range pool.entries {
		entries = append(entries, entry)
	}
	pool.entries = make(map[platformstate.Config]*stateResource)
	pool.mu.Unlock()
	var result error
	for _, entry := range entries {
		result = errors.Join(result, entry.closeResource())
	}
	return result
}

func (resource *stateResource) closeResource() error {
	if resource == nil || resource.close == nil {
		return nil
	}
	return resource.close()
}

func buildStateResource(ctx context.Context, cfg platformstate.Config) (*stateResource, error) {
	switch cfg.Kind {
	case platformstate.KindMemory:
		return &stateResource{config: cfg, store: quota.NewMemory()}, nil
	case platformstate.KindRedis:
		connectCtx, cancel := context.WithTimeout(ctx, stateConnectTimeout)
		defer cancel()
		options, err := goredis.ParseURL(cfg.URL)
		if err != nil {
			return nil, redactedStateError("parse", cfg)
		}
		client := goredis.NewClient(options)
		closeClient := func() error { return client.Close() }
		if err := client.Ping(connectCtx).Err(); err != nil {
			_ = closeClient()
			return nil, redactedStateError("connect", cfg)
		}
		store, err := quotaredis.New(client, quotaredis.Options{})
		if err == nil {
			err = store.Probe(connectCtx)
		}
		if err != nil {
			_ = closeClient()
			return nil, redactedStateError("probe", cfg)
		}
		return &stateResource{
			config: cfg,
			store:  store,
			ping:   func(ctx context.Context) error { return client.Ping(ctx).Err() },
			close:  closeClient,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported state backend %q", cfg.Kind)
	}
}

func redactedStateError(action string, cfg platformstate.Config) error {
	if cfg.Kind == platformstate.KindRedis {
		return fmt.Errorf("%s state backend %s at %s failed", action, cfg.Kind, platformstate.RedactedURL(cfg.URL))
	}
	return fmt.Errorf("%s state backend %s failed", action, cfg.Kind)
}

type stateBinding struct {
	pool   *statePool
	config platformstate.Config
	store  *quota.Switch

	mu       sync.Mutex
	resource *stateResource
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

func newStateBinding(pool *statePool, cfg platformstate.Config) *stateBinding {
	return &stateBinding{pool: pool, config: cfg, store: quota.NewUnavailableSwitch()}
}

func (binding *stateBinding) Start(ctx context.Context) error {
	if binding == nil || binding.pool == nil {
		return errors.New("state binding pool is required")
	}
	resource, err := binding.pool.acquire(ctx, binding.config)
	if err != nil {
		return err
	}
	binding.mu.Lock()
	if binding.closed {
		binding.mu.Unlock()
		_ = binding.pool.release(resource)
		return errors.New("state binding is closed")
	}
	binding.resource = resource
	binding.store.Swap(resource.store, nil, 0)
	if resource.ping != nil {
		var healthCtx context.Context
		healthCtx, binding.cancel = context.WithCancel(context.Background())
		binding.done = make(chan struct{})
		go binding.watchHealth(healthCtx, resource)
	}
	binding.mu.Unlock()
	return nil
}

func (binding *stateBinding) watchHealth(ctx context.Context, resource *stateResource) {
	defer close(binding.done)
	ticker := time.NewTicker(stateHealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := resource.ping(checkCtx)
			cancel()
			binding.store.MarkHealthy(binding.storeGeneration(), err == nil)
			if err != nil && ctx.Err() == nil {
				slog.Warn("state health check failed", "kind", binding.config.Kind)
			}
		}
	}
}

func (binding *stateBinding) storeGeneration() uint64 {
	// Each binding performs exactly one initial Swap, whose generation is one.
	return 1
}

func (binding *stateBinding) Store() quota.Store { return binding.store }

func (binding *stateBinding) Ready() bool {
	return binding != nil && binding.store != nil && binding.store.Ready()
}

func (binding *stateBinding) Close(ctx context.Context) error {
	if binding == nil {
		return nil
	}
	binding.mu.Lock()
	if binding.closed {
		binding.mu.Unlock()
		return nil
	}
	binding.closed = true
	resource, cancel, done := binding.resource, binding.cancel, binding.done
	binding.resource, binding.cancel, binding.done = nil, nil, nil
	binding.store.Shutdown()
	binding.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return errors.Join(ctx.Err(), binding.pool.release(resource))
		}
	}
	return binding.pool.release(resource)
}

func resolveStateConfig(snapshot *configsnapshot.Snapshot) (platformstate.Config, error) {
	return platformstate.LoadConfig(func(key string) (string, error) {
		if snapshot == nil {
			return "", nil
		}
		value, _ := snapshot.SettingGet(key)
		return value, nil
	})
}
