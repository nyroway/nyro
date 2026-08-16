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

func TestMemoryAdmitRequestChecksAllWindowsAndCountsOnce(t *testing.T) {
	store := NewMemory()
	clock := useFakeClock(store, time.Unix(1_000_000, 0).Truncate(time.Minute))
	limits := []RequestLimit{
		{Limit: 2, Window: time.Minute},
		{Limit: 3, Window: time.Hour},
	}

	for i := 0; i < 2; i++ {
		allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
		if err != nil || !allowed {
			t.Fatalf("admission %d = %v, %v", i, allowed, err)
		}
	}
	allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || allowed {
		t.Fatalf("third admission = %v, %v; want clean denial", allowed, err)
	}

	clock.nowValue = clock.nowValue.Add(time.Minute)
	allowed, err = store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || !allowed {
		t.Fatalf("admission after minute rollover = %v, %v", allowed, err)
	}
	allowed, err = store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || allowed {
		t.Fatalf("fourth hourly admission = %v, %v; want clean denial", allowed, err)
	}
}

func TestMemoryDeniedRequestsDoNotConsumeQuota(t *testing.T) {
	store := NewMemory()
	limits := []RequestLimit{{Limit: 1, Window: time.Minute}}
	allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || !allowed {
		t.Fatalf("first admission = %v, %v", allowed, err)
	}
	for i := 0; i < 10; i++ {
		allowed, err = store.AdmitRequest(context.Background(), "consumer", limits)
		if err != nil || allowed {
			t.Fatalf("denial %d = %v, %v", i, allowed, err)
		}
	}
	assertValue(t, store, "consumer", "requests", time.Minute, 1)
}

func TestMemoryConcurrentAdmissionIsExact(t *testing.T) {
	store := NewMemory()
	const attempts = 100
	const limit = 17
	var allowed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.AdmitRequest(context.Background(), "consumer", []RequestLimit{{Limit: limit, Window: time.Minute}})
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != limit {
		t.Fatalf("allowed = %d, want %d", got, limit)
	}
}

func TestMemoryAdmitRequestHonorsCanceledContext(t *testing.T) {
	store := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	allowed, err := store.AdmitRequest(ctx, "consumer", []RequestLimit{{Limit: 1, Window: time.Minute}})
	if !errors.Is(err, context.Canceled) || allowed {
		t.Fatalf("AdmitRequest() = %v, %v; want false, context.Canceled", allowed, err)
	}
	if len(store.rings) != 0 {
		t.Fatalf("canceled admission created %d rings", len(store.rings))
	}
}

func TestMemoryAdmitRequestWithNoLimitsDoesNotCreateRing(t *testing.T) {
	store := NewMemory()
	allowed, err := store.AdmitRequest(context.Background(), "consumer", nil)
	if err != nil || !allowed {
		t.Fatalf("AdmitRequest() = %v, %v; want true, nil", allowed, err)
	}
	if len(store.rings) != 0 {
		t.Fatalf("empty admission created %d rings", len(store.rings))
	}
}

func TestMemoryAdmitRequestValidatesBeforeCounting(t *testing.T) {
	store := NewMemory()
	invalid := []RequestLimit{
		{Limit: 2, Window: time.Minute},
		{Limit: 0, Window: time.Hour},
	}
	if allowed, err := store.AdmitRequest(context.Background(), "consumer", invalid); err == nil || allowed {
		t.Fatalf("invalid admission = %v, %v; want false and error", allowed, err)
	}
	if len(store.rings) != 0 {
		t.Fatalf("invalid admission created %d rings", len(store.rings))
	}

	valid := []RequestLimit{{Limit: 2, Window: time.Minute}}
	for i := 0; i < 2; i++ {
		allowed, err := store.AdmitRequest(context.Background(), "consumer", valid)
		if err != nil || !allowed {
			t.Fatalf("valid admission %d = %v, %v", i, allowed, err)
		}
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
