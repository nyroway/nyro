package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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
	probe  func(context.Context) error
	close  func() error
	refs   int

	healthy atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
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
		if err := verifyStateResource(ctx, existing); err != nil {
			_ = pool.release(existing)
			return nil, err
		}
		return existing, nil
	}
	pool.mu.Unlock()

	candidate, err := buildStateResource(ctx, cfg)
	if err != nil {
		return nil, err
	}
	candidate.startHealthWatcher()
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
		if err := verifyStateResource(ctx, existing); err != nil {
			_ = pool.release(existing)
			return nil, err
		}
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
	if resource == nil {
		return nil
	}
	if resource.cancel != nil {
		resource.cancel()
	}
	if resource.done != nil {
		<-resource.done
	}
	if resource.close == nil {
		return nil
	}
	return resource.close()
}

func (resource *stateResource) startHealthWatcher() {
	if resource == nil || (resource.ping == nil && resource.probe == nil) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	resource.cancel = cancel
	resource.done = make(chan struct{})
	go func() {
		defer close(resource.done)
		ticker := time.NewTicker(stateHealthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := verifyStateResource(ctx, resource); err != nil && ctx.Err() == nil {
					slog.Warn("state health check failed", "kind", resource.config.Kind)
				}
			}
		}
	}()
}

func verifyStateResource(ctx context.Context, resource *stateResource) error {
	if resource == nil {
		return errors.New("state resource is required")
	}
	if resource.ping == nil && resource.probe == nil {
		resource.healthy.Store(true)
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, stateConnectTimeout)
	defer cancel()
	if resource.ping != nil {
		if err := resource.ping(checkCtx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			resource.healthy.Store(false)
			return redactedStateError("verify", resource.config)
		}
	}
	if resource.probe != nil {
		if err := resource.probe(checkCtx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			resource.healthy.Store(false)
			return redactedStateError("verify", resource.config)
		}
	}
	resource.healthy.Store(true)
	return nil
}

func buildStateResource(ctx context.Context, cfg platformstate.Config) (*stateResource, error) {
	switch cfg.Kind {
	case platformstate.KindMemory:
		resource := &stateResource{config: cfg, store: quota.NewMemory()}
		resource.healthy.Store(true)
		return resource, nil
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
		resource := &stateResource{
			config: cfg,
			store:  store,
			ping:   func(ctx context.Context) error { return client.Ping(ctx).Err() },
			probe:  store.Probe,
			close:  closeClient,
		}
		resource.healthy.Store(true)
		return resource, nil
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

	mu       sync.Mutex
	resource *stateResource
	closed   bool
}

func newStateBinding(pool *statePool, cfg platformstate.Config) *stateBinding {
	return &stateBinding{pool: pool, config: cfg}
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
	binding.mu.Unlock()
	return nil
}

func (binding *stateBinding) Store() quota.Store { return binding }

func (binding *stateBinding) Ready() bool {
	resource := binding.currentResource()
	return resource != nil && resource.healthy.Load()
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
	resource := binding.resource
	binding.resource = nil
	binding.mu.Unlock()
	return binding.pool.release(resource)
}

func (binding *stateBinding) currentResource() *stateResource {
	if binding == nil {
		return nil
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.closed {
		return nil
	}
	return binding.resource
}

func (binding *stateBinding) backend() (*stateResource, error) {
	resource := binding.currentResource()
	if resource == nil || !resource.healthy.Load() {
		return nil, quota.ErrUnavailable
	}
	return resource, nil
}

func (binding *stateBinding) finish(resource *stateResource, err error) {
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, quota.ErrAdmissionContended) {
		resource.healthy.Store(false)
	}
}

func (binding *stateBinding) AdmitRequest(ctx context.Context, consumerID string, limits []quota.RequestLimit) (bool, error) {
	resource, err := binding.backend()
	if err != nil {
		return false, err
	}
	allowed, err := resource.store.AdmitRequest(ctx, consumerID, limits)
	binding.finish(resource, err)
	return allowed, err
}

func (binding *stateBinding) TokenValue(ctx context.Context, consumerID string, window time.Duration) (int64, error) {
	resource, err := binding.backend()
	if err != nil {
		return 0, err
	}
	value, err := resource.store.TokenValue(ctx, consumerID, window)
	binding.finish(resource, err)
	return value, err
}

func (binding *stateBinding) RecordTokens(ctx context.Context, consumerID string, tokens int64) error {
	resource, err := binding.backend()
	if err != nil {
		return err
	}
	err = resource.store.RecordTokens(ctx, consumerID, tokens)
	binding.finish(resource, err)
	return err
}

func (binding *stateBinding) Acquire(ctx context.Context, consumerID string, limit int64, leaseTTL time.Duration) (quota.Lease, bool, error) {
	resource, err := binding.backend()
	if err != nil {
		return nil, false, err
	}
	lease, allowed, err := resource.store.Acquire(ctx, consumerID, limit, leaseTTL)
	binding.finish(resource, err)
	return lease, allowed, err
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
