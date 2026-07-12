package admin

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// fakeBroadcaster records how many times Notify was called.
type fakeBroadcaster struct {
	calls int32
}

func (f *fakeBroadcaster) Notify() { atomic.AddInt32(&f.calls, 1) }
func (f *fakeBroadcaster) callsCount() int32 {
	return atomic.LoadInt32(&f.calls)
}

// TestBumpEpochNotifiesBroadcaster proves the admin write path pushes to config-sync:
// bumpEpoch (called after every config write) must invoke the registered
// broadcaster. This is the seam that makes admin config changes reach gateways.
func TestBumpEpochNotifiesBroadcaster(t *testing.T) {
	st := memory.New()
	orig := configBroadcasterVal
	t.Cleanup(func() { configBroadcasterVal = orig })

	fb := &fakeBroadcaster{}
	SetBroadcaster(fb)
	defer SetBroadcaster(orig)

	bumpEpoch(st.Storage())

	if got := fb.callsCount(); got != 1 {
		t.Errorf("broadcaster Notify calls = %d; want 1", got)
	}
	// epoch was also bumped.
	if got := epochSetting(t, st.Storage()); got != "1" {
		t.Errorf("config_epoch = %q; want 1", got)
	}

	// Without a broadcaster, bumpEpoch still bumps the epoch and is a no-op for push.
	SetBroadcaster(nil)
	bumpEpoch(st.Storage())
	if got := epochSetting(t, st.Storage()); got != "2" {
		t.Errorf("config_epoch after nil broadcaster = %q; want 2", got)
	}
}

func epochSetting(t *testing.T, s storage.Storage) string {
	t.Helper()
	v, err := s.Settings().Get("config_epoch")
	if err != nil {
		t.Fatalf("get config_epoch: %v", err)
	}
	return v
}

// fakeEpochObserver records every epoch bumpEpoch handed it.
type fakeEpochObserver struct {
	mu      sync.Mutex
	observe []int64
}

func (f *fakeEpochObserver) Observe(epoch int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observe = append(f.observe, epoch)
}

func (f *fakeEpochObserver) observed() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.observe...)
}

// TestBumpEpochPrefersEpochWatcher proves that once an EpochWatcher is wired,
// bumpEpoch drives notification through it (Observe) instead of calling the
// raw Broadcaster directly — this is the seam that lets a poller on another
// admin replica also converge on the same epoch and notify its own gateways
// exactly once.
func TestBumpEpochPrefersEpochWatcher(t *testing.T) {
	st := memory.New()
	origWatcher := epochWatcherVal
	origBroadcaster := configBroadcasterVal
	t.Cleanup(func() {
		epochWatcherVal = origWatcher
		configBroadcasterVal = origBroadcaster
	})

	fb := &fakeBroadcaster{}
	SetBroadcaster(fb)
	fo := &fakeEpochObserver{}
	SetEpochWatcher(fo)

	bumpEpoch(st.Storage())
	bumpEpoch(st.Storage())

	if got := fo.observed(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("watcher observed = %v; want [1 2]", got)
	}
	// The raw broadcaster must not be called directly once a watcher is wired
	// — the watcher is the sole decision point for whether/when to Notify.
	if got := fb.callsCount(); got != 0 {
		t.Errorf("broadcaster Notify calls = %d; want 0 (watcher should own notification)", got)
	}
}

// TestBumpEpochFallsBackToBroadcasterWithoutWatcher proves standalone/legacy
// wiring (a Broadcaster registered but no EpochWatcher) keeps working exactly
// as before this change.
func TestBumpEpochFallsBackToBroadcasterWithoutWatcher(t *testing.T) {
	st := memory.New()
	origWatcher := epochWatcherVal
	origBroadcaster := configBroadcasterVal
	t.Cleanup(func() {
		epochWatcherVal = origWatcher
		configBroadcasterVal = origBroadcaster
	})

	SetEpochWatcher(nil)
	fb := &fakeBroadcaster{}
	SetBroadcaster(fb)

	bumpEpoch(st.Storage())

	if got := fb.callsCount(); got != 1 {
		t.Errorf("broadcaster Notify calls = %d; want 1", got)
	}
}
