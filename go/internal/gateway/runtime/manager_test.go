package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
)

func TestManagerAppliesCacheSwapToStateAndTelemetry(t *testing.T) {
	cache := &configsnapshot.Cache{}
	state := &managerStateFake{}
	obs := &managerObsFake{}
	manager := newManager(cache, state, obs)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	snapshot := snapshotWith(map[string]string{"proxy.max_retries": "4"})
	cache.Swap(snapshot)
	if state.appliedSnapshot() != snapshot {
		t.Fatal("State did not receive published snapshot")
	}
	if obs.rebuildCount() != 1 {
		t.Fatalf("telemetry rebuild calls = %d, want 1", obs.rebuildCount())
	}
}

func TestManagerShutdownClearsCallbackAndJoinsErrors(t *testing.T) {
	cache := &configsnapshot.Cache{}
	stateErr := errors.New("state shutdown")
	obsErr := errors.New("telemetry shutdown")
	state := &managerStateFake{shutdownErr: stateErr}
	obs := &managerObsFake{shutdownErr: obsErr}
	manager := newManager(cache, state, obs)

	err := manager.Shutdown(context.Background())
	if !errors.Is(err, stateErr) || !errors.Is(err, obsErr) {
		t.Fatalf("Shutdown() error = %v, want both errors", err)
	}
	if state.shutdownCount() != 1 || obs.shutdownCount() != 1 {
		t.Fatalf("shutdown calls state=%d obs=%d, want 1 each", state.shutdownCount(), obs.shutdownCount())
	}

	cache.Swap((&configsnapshot.Builder{}).Build())
	if state.applyCount() != 0 || obs.rebuildCount() != 0 {
		t.Fatal("cache callback remained registered after Shutdown")
	}
	if err := manager.Shutdown(context.Background()); !errors.Is(err, stateErr) || !errors.Is(err, obsErr) {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	if state.shutdownCount() != 1 || obs.shutdownCount() != 1 {
		t.Fatal("Shutdown was not idempotent")
	}
}

type managerStateFake struct {
	mu          sync.Mutex
	applied     *configsnapshot.Snapshot
	applyCalls  int
	shutCalls   int
	shutdownErr error
}

func (f *managerStateFake) Apply(snapshot *configsnapshot.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = snapshot
	f.applyCalls++
}

func (f *managerStateFake) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutCalls++
	return f.shutdownErr
}

func (f *managerStateFake) appliedSnapshot() *configsnapshot.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied
}

func (f *managerStateFake) applyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applyCalls
}

func (f *managerStateFake) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutCalls
}

type managerObsFake struct {
	mu          sync.Mutex
	rebuilds    int
	shutCalls   int
	shutdownErr error
}

func (f *managerObsFake) rebuild() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rebuilds++
}

func (f *managerObsFake) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutCalls++
	return f.shutdownErr
}

func (f *managerObsFake) rebuildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rebuilds
}

func (f *managerObsFake) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutCalls
}
