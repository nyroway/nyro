package runtime

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
	defaultStateConnectTimeout = 3 * time.Second
	defaultStateHealthInterval = 5 * time.Second
	defaultStateRetireAfter    = 24 * time.Hour
)

type stateBackend struct {
	store  quota.Store
	ping   func(context.Context) error
	retire func()
}

type stateBackendFactory func(context.Context, platformstate.Config) (stateBackend, error)

type stateManagerOptions struct {
	factory        stateBackendFactory
	connectTimeout time.Duration
	healthInterval time.Duration
	retryDelay     func(attempt int) time.Duration
	retireAfter    time.Duration
}

// StateManager reconciles State configuration into a stable quota Switch.
type StateManager struct {
	ctx    context.Context
	cancel context.CancelFunc
	quota  *quota.Switch
	opts   stateManagerOptions

	mu            sync.Mutex
	closed        bool
	desiredSeq    uint64
	desiredCfg    platformstate.Config
	desiredSet    bool
	desiredCancel context.CancelFunc
	appliedCfg    platformstate.Config
	appliedSet    bool
	currentPing   func(context.Context) error
	currentRetire func()
	currentGen    uint64
	healthCancel  context.CancelFunc
	wg            sync.WaitGroup
	shutdownOnce  sync.Once
	shutdownDone  chan struct{}
}

func newStateManager(parent context.Context, quotaSwitch *quota.Switch, opts stateManagerOptions) *StateManager {
	if parent == nil {
		parent = context.Background()
	}
	if quotaSwitch == nil {
		quotaSwitch = quota.NewUnavailableSwitch()
	}
	if opts.factory == nil {
		opts.factory = newRedisStateBackend
	}
	if opts.connectTimeout <= 0 {
		opts.connectTimeout = defaultStateConnectTimeout
	}
	if opts.healthInterval <= 0 {
		opts.healthInterval = defaultStateHealthInterval
	}
	if opts.retryDelay == nil {
		opts.retryDelay = defaultStateRetryDelay
	}
	if opts.retireAfter <= 0 {
		opts.retireAfter = defaultStateRetireAfter
	}
	ctx, cancel := context.WithCancel(parent)
	return &StateManager{
		ctx:          ctx,
		cancel:       cancel,
		quota:        quotaSwitch,
		opts:         opts,
		shutdownDone: make(chan struct{}),
	}
}

// Apply asynchronously reconciles a config-sync snapshot. Valid Redis
// candidates retry without replacing the last-known-good backend.
func (m *StateManager) Apply(snapshot *configsnapshot.Snapshot) {
	cfg, err := resolveStateConfig(snapshot)
	if err != nil {
		m.invalidateDesired()
		slog.Error("state config rejected; keeping current backend", "error", err)
		return
	}
	if cfg.Kind == platformstate.KindMemory {
		sequence, _, ok := m.beginDesired(cfg, false)
		if ok {
			m.install(sequence, cfg, stateBackend{store: quota.NewMemory()})
		}
		return
	}

	sequence, candidateCtx, ok := m.beginDesired(cfg, true)
	if !ok {
		return
	}
	go m.retryCandidate(candidateCtx, sequence, cfg)
}

// ApplyStrict reconciles one standalone snapshot and returns the first
// connection failure instead of retrying or substituting Memory.
func (m *StateManager) ApplyStrict(ctx context.Context, snapshot *configsnapshot.Snapshot) error {
	cfg, err := resolveStateConfig(snapshot)
	if err != nil {
		return err
	}
	sequence, candidateCtx, ok := m.beginDesired(cfg, false)
	if !ok {
		return errors.New("state manager is shut down")
	}
	if cfg.Kind == platformstate.KindMemory {
		if !m.install(sequence, cfg, stateBackend{store: quota.NewMemory()}) {
			return errors.New("state configuration was superseded")
		}
		return nil
	}

	parent := candidateCtx
	if ctx != nil {
		var cancel context.CancelFunc
		parent, cancel = context.WithCancel(candidateCtx)
		defer cancel()
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-parent.Done():
			}
		}()
	}
	backend, err := m.buildBackend(parent, cfg)
	if err != nil {
		return redactedStateFailure("initialize", cfg)
	}
	if !m.install(sequence, cfg, backend) {
		retireBackend(backend)
		return errors.New("state configuration was superseded")
	}
	return nil
}

func (m *StateManager) beginDesired(cfg platformstate.Config, asynchronous bool) (uint64, context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, nil, false
	}
	if m.desiredSet && m.desiredCfg == cfg {
		return 0, nil, false
	}
	if m.desiredCancel != nil {
		m.desiredCancel()
	}
	m.desiredSeq++
	m.desiredCfg = cfg
	m.desiredSet = true
	candidateCtx, cancel := context.WithCancel(m.ctx)
	m.desiredCancel = cancel
	if asynchronous {
		m.wg.Add(1)
	}
	return m.desiredSeq, candidateCtx, true
}

func (m *StateManager) invalidateDesired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.desiredCancel != nil {
		m.desiredCancel()
		m.desiredCancel = nil
	}
	m.desiredSeq++
	m.desiredSet = false
}

func (m *StateManager) retryCandidate(ctx context.Context, sequence uint64, cfg platformstate.Config) {
	defer m.wg.Done()
	for attempt := 1; ; attempt++ {
		backend, err := m.buildBackend(ctx, cfg)
		if err == nil {
			if !m.install(sequence, cfg, backend) {
				retireBackend(backend)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("state candidate unavailable; retrying",
			"kind", cfg.Kind,
			"url", platformstate.RedactedURL(cfg.URL),
			"attempt", attempt,
		)
		timer := time.NewTimer(m.opts.retryDelay(attempt))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *StateManager) buildBackend(parent context.Context, cfg platformstate.Config) (stateBackend, error) {
	ctx, cancel := context.WithTimeout(parent, m.opts.connectTimeout)
	defer cancel()
	backend, err := m.opts.factory(ctx, cfg)
	if err != nil {
		retireBackend(backend)
		return stateBackend{}, err
	}
	if backend.store == nil {
		retireBackend(backend)
		return stateBackend{}, errors.New("state backend has no quota Store")
	}
	return backend, nil
}

func (m *StateManager) install(sequence uint64, cfg platformstate.Config, backend stateBackend) bool {
	m.mu.Lock()
	if m.closed || !m.desiredSet || m.desiredSeq != sequence || m.desiredCfg != cfg {
		m.mu.Unlock()
		return false
	}
	if m.healthCancel != nil {
		m.healthCancel()
		m.healthCancel = nil
	}
	oldRetire := m.currentRetire
	generation := m.quota.Swap(backend.store, oldRetire, m.opts.retireAfter)
	m.appliedCfg = cfg
	m.appliedSet = true
	m.currentPing = backend.ping
	m.currentRetire = backend.retire
	m.currentGen = generation
	if m.desiredCancel != nil {
		m.desiredCancel()
		m.desiredCancel = nil
	}

	var healthCtx context.Context
	if backend.ping != nil {
		healthCtx, m.healthCancel = context.WithCancel(m.ctx)
		m.wg.Add(1)
	}
	m.mu.Unlock()

	if backend.ping != nil {
		go m.healthLoop(healthCtx, generation, backend.ping)
	}
	return true
}

func (m *StateManager) healthLoop(ctx context.Context, generation uint64, ping func(context.Context) error) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.opts.healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, m.opts.connectTimeout)
			err := ping(pingCtx)
			cancel()
			m.quota.MarkHealthy(generation, err == nil)
			if err != nil && ctx.Err() == nil {
				slog.Warn("state health check failed", "generation", generation)
			}
		}
	}
}

// Shutdown stops candidate and health work, then retires the active backend.
func (m *StateManager) Shutdown(ctx context.Context) error {
	m.shutdownOnce.Do(func() {
		go m.shutdown()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *StateManager) shutdown() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	if m.desiredCancel != nil {
		m.desiredCancel()
		m.desiredCancel = nil
	}
	if m.healthCancel != nil {
		m.healthCancel()
		m.healthCancel = nil
	}
	currentRetire := m.currentRetire
	m.currentRetire = nil
	m.mu.Unlock()

	m.wg.Wait()
	m.quota.Swap(nil, currentRetire, m.opts.retireAfter)
	m.quota.Shutdown()
	close(m.shutdownDone)
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

func newRedisStateBackend(ctx context.Context, cfg platformstate.Config) (stateBackend, error) {
	options, err := goredis.ParseURL(cfg.URL)
	if err != nil {
		return stateBackend{}, err
	}
	client := goredis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return stateBackend{}, err
	}
	store, err := quotaredis.New(client, quotaredis.Options{})
	if err == nil {
		err = store.Probe(ctx)
	}
	if err != nil {
		_ = client.Close()
		return stateBackend{}, err
	}
	return stateBackend{
		store: store,
		ping:  func(ctx context.Context) error { return client.Ping(ctx).Err() },
		retire: func() {
			if err := client.Close(); err != nil {
				slog.Warn("state Redis client close failed",
					"url", platformstate.RedactedURL(cfg.URL),
					"error", err,
				)
			}
		},
	}, nil
}

func retireBackend(backend stateBackend) {
	if backend.retire != nil {
		backend.retire()
	}
}

func redactedStateFailure(action string, cfg platformstate.Config) error {
	if cfg.Kind == platformstate.KindRedis {
		return fmt.Errorf("%s state backend %s at %s failed", action, cfg.Kind, platformstate.RedactedURL(cfg.URL))
	}
	return fmt.Errorf("%s state backend %s failed", action, cfg.Kind)
}

func defaultStateRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
