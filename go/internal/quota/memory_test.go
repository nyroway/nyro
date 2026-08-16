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

func TestMemoryRecordsTokensAndReadsWindows(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	clock := useFakeClock(store, time.Unix(1_000_000, 0).Truncate(time.Minute))

	if err := store.RecordTokens(ctx, "k1", 150); err != nil {
		t.Fatal(err)
	}
	assertTokenValue(t, store, "k1", time.Minute, 150)
	assertTokenValue(t, store, "k1", MaxWindow, 150)
	assertTokenValue(t, store, "missing", time.Minute, 0)

	clock.nowValue = clock.nowValue.Add(5 * time.Second)
	assertTokenValue(t, store, "k1", time.Minute, 150)
}

func TestMemoryWindowRolloff(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Unix(2_000_000, 0).Truncate(time.Minute)
	clock := useFakeClock(store, base)

	for i := 0; i < 60; i++ {
		clock.nowValue = base.Add(time.Duration(i) * time.Minute)
		if err := store.RecordTokens(ctx, "k", 1); err != nil {
			t.Fatal(err)
		}
	}
	assertTokenValue(t, store, "k", time.Hour, 60)

	clock.nowValue = base.Add(60 * time.Minute)
	assertTokenValue(t, store, "k", time.Hour, 59)

	clock.nowValue = base.Add(25 * time.Hour)
	assertTokenValue(t, store, "k", MaxWindow, 0)
}

func TestMemoryMinuteAndDayWindowsAreIndependent(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Unix(1_000_000, 0).Truncate(time.Minute)
	clock := useFakeClock(store, base)

	if err := store.RecordTokens(ctx, "k", 100); err != nil {
		t.Fatal(err)
	}
	clock.nowValue = base.Add(90 * time.Minute)

	assertTokenValue(t, store, "k", time.Minute, 0)
	assertTokenValue(t, store, "k", MaxWindow, 100)
}

func TestMemoryConcurrentRecordTokensAndTokenValue(t *testing.T) {
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
				if err := store.RecordTokens(ctx, "k", 3); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := store.TokenValue(ctx, "k", time.Minute); err != nil {
					t.Error(err)
					return
				}
			}
			readersCompleted.Store(true)
		}()
	}
	wg.Wait()

	assertTokenValue(t, store, "k", time.Minute, int64(goroutines*perGoroutine*3))
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
	clock := useFakeClock(store, time.Unix(1_000_000, 0).Truncate(time.Minute))
	limits := []RequestLimit{{Limit: 1, Window: time.Minute}, {Limit: 2, Window: time.Hour}}
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
	clock.nowValue = clock.nowValue.Add(time.Minute)
	allowed, err = store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || !allowed {
		t.Fatalf("admission after denials = %v, %v; denials consumed quota", allowed, err)
	}
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

	if err := store.RecordTokens(ctx, "idle", 1); err != nil {
		t.Fatal(err)
	}
	clock.nowValue = base.Add(25 * time.Hour)
	if err := store.RecordTokens(ctx, "active", 1); err != nil {
		t.Fatal(err)
	}

	if got := store.GC(); got != 1 {
		t.Fatalf("GC() = %d, want 1", got)
	}
	store.mu.Lock()
	_, idlePresent := store.rings[quotaKey{"idle", "tokens"}]
	_, activePresent := store.rings[quotaKey{"active", "tokens"}]
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

	if err := store.RecordTokens(ctx, "k", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("RecordTokens() error = %v, want context.Canceled", err)
	}
	if _, err := store.TokenValue(ctx, "k", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("TokenValue() error = %v, want context.Canceled", err)
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

func assertTokenValue(t *testing.T, store Store, consumerID string, window time.Duration, want int64) {
	t.Helper()
	got, err := store.TokenValue(context.Background(), consumerID, window)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("TokenValue(%q, %v) = %d, want %d", consumerID, window, got, want)
	}
}
