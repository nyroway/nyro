package quota

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func useFakeClock(m *Memory, now time.Time) *fakeClock {
	clock := &fakeClock{nowValue: now}
	m.now = clock.Now
	return clock
}

type fakeClock struct {
	nowValue time.Time
}

func (f *fakeClock) Now() time.Time {
	return f.nowValue
}

func TestMemoryRecordsUsageAndReadsWindows(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	clock := useFakeClock(store, time.Unix(1_000_000, 0).Truncate(time.Minute))

	if err := store.Record(ctx, "k1", Usage{Requests: 1, Tokens: 150}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "k1", Usage{Requests: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "k2", Usage{Requests: 1}); err != nil {
		t.Fatal(err)
	}

	assertValue(t, store, "k1", "requests", time.Minute, 2)
	assertValue(t, store, "k1", "tokens", time.Minute, 150)
	assertValue(t, store, "k1", "requests", MaxWindow, 2)
	assertValue(t, store, "k2", "requests", time.Minute, 1)
	assertValue(t, store, "missing", "requests", time.Minute, 0)

	clock.nowValue = clock.nowValue.Add(5 * time.Second)
	assertValue(t, store, "k1", "requests", time.Minute, 2)
}

func TestMemoryWindowRolloff(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Unix(2_000_000, 0).Truncate(time.Minute)
	clock := useFakeClock(store, base)

	for i := 0; i < 60; i++ {
		clock.nowValue = base.Add(time.Duration(i) * time.Minute)
		if err := store.Record(ctx, "k", Usage{Requests: 1}); err != nil {
			t.Fatal(err)
		}
	}
	assertValue(t, store, "k", "requests", time.Hour, 60)

	clock.nowValue = base.Add(60 * time.Minute)
	assertValue(t, store, "k", "requests", time.Hour, 59)

	clock.nowValue = base.Add(25 * time.Hour)
	assertValue(t, store, "k", "requests", MaxWindow, 0)
}

func TestMemoryMinuteAndDayWindowsAreIndependent(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Unix(1_000_000, 0).Truncate(time.Minute)
	clock := useFakeClock(store, base)

	if err := store.Record(ctx, "k", Usage{Requests: 1, Tokens: 100}); err != nil {
		t.Fatal(err)
	}
	clock.nowValue = base.Add(90 * time.Minute)

	assertValue(t, store, "k", "requests", time.Minute, 0)
	assertValue(t, store, "k", "requests", MaxWindow, 1)
	assertValue(t, store, "k", "tokens", MaxWindow, 100)
}

func TestMemoryConcurrentRecordAndValue(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	const goroutines = 50
	const perGoroutine = 200
	var wg sync.WaitGroup
	var readersCompleted atomic.Bool
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if err := store.Record(ctx, "k", Usage{Requests: 1, Tokens: 3}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := store.Value(ctx, "k", "requests", time.Minute); err != nil {
					t.Error(err)
					return
				}
			}
			readersCompleted.Store(true)
		}()
	}
	wg.Wait()

	assertValue(t, store, "k", "requests", time.Minute, int64(goroutines*perGoroutine))
	assertValue(t, store, "k", "tokens", time.Minute, int64(goroutines*perGoroutine*3))
	if !readersCompleted.Load() {
		t.Fatal("readers did not complete")
	}
}

func TestMemoryGCDropsExpiredRings(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Unix(1_000_000, 0).Truncate(time.Minute)
	clock := useFakeClock(store, base)

	if err := store.Record(ctx, "idle", Usage{Requests: 1}); err != nil {
		t.Fatal(err)
	}
	clock.nowValue = base.Add(25 * time.Hour)
	if err := store.Record(ctx, "active", Usage{Requests: 1}); err != nil {
		t.Fatal(err)
	}

	if got := store.GC(); got != 1 {
		t.Fatalf("GC() = %d, want 1", got)
	}
	store.mu.Lock()
	_, idlePresent := store.rings[quotaKey{"idle", "requests"}]
	_, activePresent := store.rings[quotaKey{"active", "requests"}]
	store.mu.Unlock()
	if idlePresent || !activePresent {
		t.Fatalf("rings after GC: idle=%v active=%v", idlePresent, activePresent)
	}
}

func TestMemoryAcquireReturnsIdempotentLease(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()

	first, allowed, err := store.Acquire(ctx, "k1", 1, 5*time.Minute)
	if err != nil || !allowed || first == nil {
		t.Fatalf("first Acquire() = lease %v, allowed %v, error %v", first, allowed, err)
	}
	_, allowed, err = store.Acquire(ctx, "k1", 1, 5*time.Minute)
	if err != nil || allowed {
		t.Fatalf("second Acquire() = allowed %v, error %v", allowed, err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	second, allowed, err := store.Acquire(ctx, "k1", 1, 5*time.Minute)
	if err != nil || !allowed || second == nil {
		t.Fatalf("Acquire() after release = lease %v, allowed %v, error %v", second, allowed, err)
	}
}

func TestMemoryHonorsCanceledContext(t *testing.T) {
	store := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Record(ctx, "k", Usage{Requests: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record() error = %v, want context.Canceled", err)
	}
	if _, err := store.Value(ctx, "k", "requests", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("Value() error = %v, want context.Canceled", err)
	}
	if _, _, err := store.Acquire(ctx, "k", 1, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v, want context.Canceled", err)
	}
}

func TestParseWindow(t *testing.T) {
	cases := map[string]time.Duration{
		"1m": time.Minute,
		"1h": time.Hour,
		"1d": 24 * time.Hour,
		"2d": 48 * time.Hour,
	}
	for raw, want := range cases {
		got, err := ParseWindow(raw)
		if err != nil {
			t.Fatalf("ParseWindow(%q): %v", raw, err)
		}
		if got != want {
			t.Errorf("ParseWindow(%q) = %v, want %v", raw, got, want)
		}
	}
}

func assertValue(t *testing.T, store Store, consumerID, quotaType string, window time.Duration, want int64) {
	t.Helper()
	got, err := store.Value(context.Background(), consumerID, quotaType, window)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Value(%q, %q, %v) = %d, want %d", consumerID, quotaType, window, got, want)
	}
}
