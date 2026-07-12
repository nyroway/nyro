package configsync

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNotifier records how many times Notify was called.
type fakeNotifier struct {
	calls int32
}

func (f *fakeNotifier) Notify() { atomic.AddInt32(&f.calls, 1) }
func (f *fakeNotifier) callsCount() int32 {
	return atomic.LoadInt32(&f.calls)
}

// fakeEpochStore is an in-memory EpochStore for tests.
type fakeEpochStore struct {
	mu    sync.Mutex
	value string
}

func (f *fakeEpochStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, nil
}

func (f *fakeEpochStore) set(v int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = strconv.FormatInt(v, 10)
}

func TestEpochWatcher_ObserveSkipsNonIncreasing(t *testing.T) {
	n := &fakeNotifier{}
	w := NewEpochWatcher(&fakeEpochStore{}, n)

	w.Observe(5)
	if got := n.callsCount(); got != 1 {
		t.Fatalf("Notify calls after first Observe(5) = %d; want 1", got)
	}

	w.Observe(5)
	w.Observe(3)
	if got := n.callsCount(); got != 1 {
		t.Fatalf("Notify calls after Observe(5), Observe(3) = %d; want 1 (no-op for <= lastEpoch)", got)
	}

	w.Observe(6)
	if got := n.callsCount(); got != 2 {
		t.Fatalf("Notify calls after Observe(6) = %d; want 2", got)
	}
}

func TestEpochWatcher_ObserveConcurrentSameEpochNotifiesOnce(t *testing.T) {
	n := &fakeNotifier{}
	w := NewEpochWatcher(&fakeEpochStore{}, n)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			w.Observe(1)
		})
	}
	wg.Wait()

	if got := n.callsCount(); got != 1 {
		t.Fatalf("Notify calls after 50 concurrent Observe(1) = %d; want 1", got)
	}
}

func TestEpochWatcher_SeedDoesNotNotify(t *testing.T) {
	n := &fakeNotifier{}
	store := &fakeEpochStore{}
	store.set(42)
	w := NewEpochWatcher(store, n)

	if err := w.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if got := n.callsCount(); got != 0 {
		t.Fatalf("Notify calls after Seed = %d; want 0", got)
	}

	// A subsequent Observe at or below the seeded epoch is still a no-op.
	w.Observe(42)
	if got := n.callsCount(); got != 0 {
		t.Fatalf("Notify calls after Observe(seeded epoch) = %d; want 0", got)
	}

	// But an epoch beyond the seed still triggers exactly one push.
	w.Observe(43)
	if got := n.callsCount(); got != 1 {
		t.Fatalf("Notify calls after Observe(43) = %d; want 1", got)
	}
}

func TestEpochWatcher_RunPollsAndObserves(t *testing.T) {
	n := &fakeNotifier{}
	store := &fakeEpochStore{}
	store.set(1)
	w := NewEpochWatcher(store, n)
	if err := w.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, 5*time.Millisecond)
		close(done)
	}()

	// Simulate a remote replica bumping the shared epoch.
	store.set(2)

	deadline := time.After(time.Second)
	for n.callsCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Run to observe the new epoch")
		case <-time.After(time.Millisecond):
		}
	}
	if got := n.callsCount(); got != 1 {
		t.Fatalf("Notify calls after epoch bump = %d; want 1", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestEpochWatcher_RunZeroIntervalDisablesPolling(t *testing.T) {
	n := &fakeNotifier{}
	store := &fakeEpochStore{}
	store.set(1)
	w := NewEpochWatcher(store, n)

	done := make(chan struct{})
	go func() {
		w.Run(context.Background(), 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run(interval=0) did not return immediately")
	}
}
