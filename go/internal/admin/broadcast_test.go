package admin

import (
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

// TestBumpEpochNotifiesBroadcaster proves the admin write path pushes to xDS:
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
