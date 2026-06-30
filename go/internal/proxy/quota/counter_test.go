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
	fc, _ := useFakeClock(c, time.Unix(1_000_000, 0))

	c.Record("k1", 1, 100)
	c.Record("k1", 1, 50)
	c.Record("k2", 1, 7)

	// Same instant → both windows accumulate.
	if got := c.Requests("k1", WindowMinute); got != 2 {
		t.Errorf("k1 minute requests = %d, want 2", got)
	}
	if got := c.Tokens("k1", WindowMinute); got != 150 {
		t.Errorf("k1 minute tokens = %d, want 150", got)
	}
	if got := c.Requests("k1", WindowDay); got != 2 {
		t.Errorf("k1 day requests = %d, want 2", got)
	}
	if got := c.Requests("k2", WindowMinute); got != 1 {
		t.Errorf("k2 minute requests = %d, want 1", got)
	}
	// Unknown key → 0.
	if got := c.Requests("nope", WindowMinute); got != 0 {
		t.Errorf("unknown key requests = %d, want 0", got)
	}

	// 60-second window: 1s advance keeps second 0 in-window (window = [t-59, t]).
	fc.t = fc.t.Add(time.Second)
	if got := c.Requests("k1", WindowMinute); got != 2 {
		t.Errorf("k1 after 1s advance = %d, want 2 (still in 60s window)", got)
	}

	// 61 seconds after the write (which was at t0): second 0 rolls off.
	fc.t = time.Unix(1_000_000, 0).Add(61 * time.Second)
	if got := c.Requests("k1", WindowMinute); got != 0 {
		t.Errorf("k1 after 61s advance = %d, want 0", got)
	}
}

func TestMinuteWindowExpiry(t *testing.T) {
	c := New()
	base := time.Unix(2_000_000, 0)
	fc, _ := useFakeClock(c, base)

	// 60 records, one per second, seconds 0..59. Do NOT advance past the last
	// record before reading (advancing would roll second 0 off early).
	for i := 0; i < 60; i++ {
		fc.t = base.Add(time.Duration(i) * time.Second)
		c.Record("k", 1, 10)
	}
	// Window covers seconds [0, 59] → all 60 buckets present.
	if got := c.Requests("k", WindowMinute); got != 60 {
		t.Errorf("full window = %d, want 60", got)
	}
	// One more second (now=60): second 0 drops out, but we add nothing, so the
	// window holds seconds [1, 60] = 59 live buckets (second 60 bucket empty).
	fc.t = base.Add(60 * time.Second)
	if got := c.Requests("k", WindowMinute); got != 59 {
		t.Errorf("after rolling second 0 = %d, want 59", got)
	}
	// Wait out the whole 60-second window from the last write (second 59).
	fc.t = base.Add(120 * time.Second)
	if got := c.Requests("k", WindowMinute); got != 0 {
		t.Errorf("after full minute idle = %d, want 0", got)
	}
}

func TestDayWindowExpiry(t *testing.T) {
	c := New()
	base := time.Unix(0, 0).UTC() // hour 0 epoch
	fc, _ := useFakeClock(c, base)

	c.Record("k", 1, 100)
	// 25 hours later: day window (24 buckets) must have rolled off entirely.
	fc.t = base.Add(25 * time.Hour)
	if got := c.Requests("k", WindowDay); got != 0 {
		t.Errorf("day window after 25h = %d, want 0", got)
	}
}

func TestMinuteAndDayIndependent(t *testing.T) {
	c := New()
	base := time.Unix(1_000_000, 0)
	fc, _ := useFakeClock(c, base)

	// One record at hour 0.
	c.Record("k", 1, 100)
	// 90 minutes later: minute window empty, day window still holds it.
	fc.t = base.Add(90 * time.Minute)
	if got := c.Requests("k", WindowMinute); got != 0 {
		t.Errorf("minute after 90m = %d, want 0", got)
	}
	if got := c.Requests("k", WindowDay); got != 1 {
		t.Errorf("day after 90m = %d, want 1", got)
	}
	if got := c.Tokens("k", WindowDay); got != 100 {
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
				c.Record("k", 1, 3)
			}
		}()
	}
	// Concurrent readers (must not race / panic).
	var readOK int64
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				_ = c.Requests("k", WindowMinute)
				_ = c.Tokens("k", WindowDay)
			}
			atomic.StoreInt64(&readOK, 1)
		}()
	}
	wg.Wait()
	if got := c.Requests("k", WindowMinute); got != int64(goroutines*perG) {
		t.Errorf("concurrent requests = %d, want %d", got, goroutines*perG)
	}
	if got := c.Tokens("k", WindowMinute); got != int64(goroutines*perG*3) {
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

	c.Record("idle", 1, 10)

	// Let the idle key's BOTH windows fully expire (>24h for the day window).
	fc.t = base.Add(25 * time.Hour)

	// Now record an active key at the current time — it must survive GC.
	c.Record("active", 1, 10)

	removed := c.GC()
	if removed != 1 {
		t.Fatalf("GC removed %d keys, want 1", removed)
	}
	c.mu.Lock()
	_, idlePresent := c.state["idle"]
	_, activePresent := c.state["active"]
	c.mu.Unlock()
	if idlePresent {
		t.Error("idle key still present after GC")
	}
	if !activePresent {
		t.Error("active key removed by GC (should stay)")
	}
}
