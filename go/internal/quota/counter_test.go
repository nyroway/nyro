package quota

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// useFakeClock swaps the counter's clock and returns a restore func.
func useFakeClock(c *Counter, t time.Time) (*fakeClock, func()) {
	fc := &fakeClock{t: t}
	prev := c.now
	c.now = fc.now
	return fc, func() { c.now = prev }
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func TestRecordAndRead(t *testing.T) {
	c := New()
	// Minute-aligned so a few-second advance stays in the same 1m bucket
	// (bucketResolution == time.Minute, so a 1-minute window is exactly one
	// bucket wide — no partial-overlap credit across a bucket boundary).
	fc, _ := useFakeClock(c, time.Unix(1_000_000, 0).Truncate(time.Minute))

	c.Record("k1", "requests", 1)
	c.Record("k1", "requests", 1)
	c.Record("k1", "tokens", 150)
	c.Record("k2", "requests", 1)

	// Same instant → both windows accumulate.
	if got := c.Value("k1", "requests", time.Minute); got != 2 {
		t.Errorf("k1 minute requests = %d, want 2", got)
	}
	if got := c.Value("k1", "tokens", time.Minute); got != 150 {
		t.Errorf("k1 minute tokens = %d, want 150", got)
	}
	if got := c.Value("k1", "requests", 24*time.Hour); got != 2 {
		t.Errorf("k1 day requests = %d, want 2", got)
	}
	if got := c.Value("k2", "requests", time.Minute); got != 1 {
		t.Errorf("k2 minute requests = %d, want 1", got)
	}
	// Unknown key → 0.
	if got := c.Value("nope", "requests", time.Minute); got != 0 {
		t.Errorf("unknown key requests = %d, want 0", got)
	}

	// A few seconds later, still inside the same 1-minute bucket.
	fc.t = fc.t.Add(5 * time.Second)
	if got := c.Value("k1", "requests", time.Minute); got != 2 {
		t.Errorf("k1 after 5s advance = %d, want 2 (still in 1m window)", got)
	}
}

func TestMinuteWindowExpiry(t *testing.T) {
	c := New()
	base := time.Unix(2_000_000, 0)
	fc, _ := useFakeClock(c, base)

	// 60 records, one per minute, minutes 0..59 — spans the whole 1h ring
	// window used to validate rolloff at coarser granularity.
	for i := 0; i < 60; i++ {
		fc.t = base.Add(time.Duration(i) * time.Minute)
		c.Record("k", "requests", 1)
	}
	// Window covers the trailing hour → all 60 one-minute buckets present.
	if got := c.Value("k", "requests", time.Hour); got != 60 {
		t.Errorf("full window = %d, want 60", got)
	}
	// One more minute: the oldest bucket (minute 0) drops out of the 1h window.
	fc.t = base.Add(60 * time.Minute)
	if got := c.Value("k", "requests", time.Hour); got != 59 {
		t.Errorf("after rolling first minute = %d, want 59", got)
	}
}

func TestDayWindowExpiry(t *testing.T) {
	c := New()
	base := time.Unix(0, 0).UTC() // hour 0 epoch
	fc, _ := useFakeClock(c, base)

	c.Record("k", "requests", 1)
	// 25 hours later: day window (24h) must have rolled off entirely.
	fc.t = base.Add(25 * time.Hour)
	if got := c.Value("k", "requests", 24*time.Hour); got != 0 {
		t.Errorf("day window after 25h = %d, want 0", got)
	}
}

func TestMinuteAndDayIndependent(t *testing.T) {
	c := New()
	base := time.Unix(1_000_000, 0)
	fc, _ := useFakeClock(c, base)

	// One record at t0.
	c.Record("k", "requests", 1)
	c.Record("k", "tokens", 100)
	// 90 minutes later: 1-minute window empty, 24h window still holds it.
	fc.t = base.Add(90 * time.Minute)
	if got := c.Value("k", "requests", time.Minute); got != 0 {
		t.Errorf("minute after 90m = %d, want 0", got)
	}
	if got := c.Value("k", "requests", 24*time.Hour); got != 1 {
		t.Errorf("day after 90m = %d, want 1", got)
	}
	if got := c.Value("k", "tokens", 24*time.Hour); got != 100 {
		t.Errorf("day tokens after 90m = %d, want 100", got)
	}
}

func TestConcurrent(t *testing.T) {
	c := New()
	const goroutines = 50
	const perG = 200
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	// Writers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c.Record("k", "requests", 1)
				c.Record("k", "tokens", 3)
			}
		}()
	}
	// Concurrent readers (must not race / panic).
	var readOK int64
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				_ = c.Value("k", "requests", time.Minute)
				_ = c.Value("k", "tokens", 24*time.Hour)
			}
			atomic.StoreInt64(&readOK, 1)
		}()
	}
	wg.Wait()
	if got := c.Value("k", "requests", time.Minute); got != int64(goroutines*perG) {
		t.Errorf("concurrent requests = %d, want %d", got, goroutines*perG)
	}
	if got := c.Value("k", "tokens", time.Minute); got != int64(goroutines*perG*3) {
		t.Errorf("concurrent tokens = %d, want %d", got, goroutines*perG*3)
	}
	if atomic.LoadInt64(&readOK) == 0 {
		t.Error("readers did not complete")
	}
}

func TestGC(t *testing.T) {
	c := New()
	base := time.Unix(1_000_000, 0)
	fc, _ := useFakeClock(c, base)

	c.Record("idle", "requests", 1)

	// Let the idle key's ring fully expire (>24h, the ring's full span).
	fc.t = base.Add(25 * time.Hour)

	// Now record an active key at the current time — it must survive GC.
	c.Record("active", "requests", 1)

	removed := c.GC()
	if removed != 1 {
		t.Fatalf("GC removed %d keys, want 1", removed)
	}
	c.mu.Lock()
	_, idlePresent := c.rings[quotaKey{"idle", "requests"}]
	_, activePresent := c.rings[quotaKey{"active", "requests"}]
	c.mu.Unlock()
	if idlePresent {
		t.Error("idle key still present after GC")
	}
	if !activePresent {
		t.Error("active key removed by GC (should stay)")
	}
}

func TestConcurrencyAcquireRelease(t *testing.T) {
	c := New()
	if !c.TryAcquire("k1", 2) {
		t.Fatal("first acquire should succeed")
	}
	if !c.TryAcquire("k1", 2) {
		t.Fatal("second acquire should succeed")
	}
	if c.TryAcquire("k1", 2) {
		t.Fatal("third acquire should fail at limit 2")
	}
	c.Release("k1")
	if !c.TryAcquire("k1", 2) {
		t.Fatal("acquire after release should succeed")
	}
	// Independent consumers don't share slots.
	if !c.TryAcquire("k2", 1) {
		t.Fatal("k2 first acquire should succeed")
	}
	// Release never underflows.
	c.Release("k1")
	c.Release("k1")
	c.Release("k1")
	if !c.TryAcquire("k1", 1) {
		t.Fatal("acquire after over-release should still succeed")
	}
}

func TestParseWindow(t *testing.T) {
	cases := map[string]time.Duration{
		"1m": time.Minute,
		"1h": time.Hour,
		"1d": 24 * time.Hour,
		"2d": 48 * time.Hour,
	}
	for s, want := range cases {
		got, err := ParseWindow(s)
		if err != nil {
			t.Fatalf("ParseWindow(%q): %v", s, err)
		}
		if got != want {
			t.Errorf("ParseWindow(%q) = %v, want %v", s, got, want)
		}
	}
}
