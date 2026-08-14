package runtime

import (
	"context"
	"errors"
	"sync"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
)

type stateLifecycle interface {
	Apply(*configsnapshot.Snapshot)
	Shutdown(context.Context) error
}

type observabilityLifecycle interface {
	rebuild()
	Shutdown(context.Context) error
}

// Manager owns all hot-reloadable Gateway runtime resources.
type Manager struct {
	cache *configsnapshot.Cache
	state stateLifecycle
	obs   observabilityLifecycle

	mu           sync.Mutex
	closed       bool
	shutdownOnce sync.Once
	shutdownErr  error
}

func newManager(cache *configsnapshot.Cache, state stateLifecycle, obs observabilityLifecycle) *Manager {
	manager := &Manager{cache: cache, state: state, obs: obs}
	if cache != nil {
		cache.SetOnSwap(manager.apply)
	}
	return manager
}

func (m *Manager) apply() {
	if m == nil || m.cache == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	snapshot := m.cache.Load()
	if m.state != nil {
		m.state.Apply(snapshot)
	}
	if m.obs != nil {
		m.obs.rebuild()
	}
}

// Shutdown clears the cache callback before stopping State and telemetry.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		if m.cache != nil {
			m.cache.SetOnSwap(nil)
		}
		m.mu.Unlock()
		var stateErr, obsErr error
		if m.state != nil {
			stateErr = m.state.Shutdown(ctx)
		}
		if m.obs != nil {
			obsErr = m.obs.Shutdown(ctx)
		}
		m.shutdownErr = errors.Join(stateErr, obsErr)
	})
	return m.shutdownErr
}
