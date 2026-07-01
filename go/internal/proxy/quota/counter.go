// Package quota is the in-memory sliding-window counter for consumer quotas
// (config-schema: arbitrary {quota_type, window} pairs — e.g. requests/1m,
// tokens/1d — rather than fixed RPM/RPD/TPM/TPD slots). It replaces the
// request_logs scan that the inbound access check used to perform on every
// authenticated request.
//
// The counter is per-gateway-process and in-memory: a restart loses all
// counts. Quotas are soft limits (they bound concurrent usage, not
// correctness), so this is acceptable — the worst case after a restart is a
// brief over-admission until the windows refill.
//
// Design: each (consumerID, quotaType) pair gets one fixed-resolution ring
// spanning maxWindow (buckets of bucketResolution). Record doesn't need to
// know which window(s) a consumer's quotas use — it just adds to the ring.
// Value(window) sums the trailing window's worth of buckets from that same
// ring, so a consumer can have several quotas of the same type at different
// windows (e.g. requests/1m and requests/1d) sharing one ring.
package quota

import (
	"sync"
	"time"
)

// bucketResolution is the width of one bucket; maxWindow/bucketResolution is
// the ring size. 1-minute buckets over 24h give both minute- and day-scale
// quotas reasonable precision without unbounded memory.
const (
	bucketResolution = time.Minute
	maxWindow        = 24 * time.Hour
	ringSize         = int(maxWindow / bucketResolution)
)

// bucket holds a running total for one time slot.
type bucket struct {
	value int64
}

// ring is a fixed-size circular buffer covering the trailing maxWindow.
type ring struct {
	// epoch is the absolute bucket index (minutes since Unix epoch) of the
	// most recently touched slot. It advances on write/read so stale buckets
	// can be zeroed in place.
	epoch   int64
	buckets [ringSize]bucket
}

func indexFor(t time.Time) int64 { return t.UnixNano() / int64(bucketResolution) }

// advance zeroes any buckets that fall out of the window ending at target and
// moves the ring's epoch forward to target. Called under the counter mutex.
func (r *ring) advance(target int64) {
	if target <= r.epoch {
		return
	}
	if target-r.epoch >= int64(len(r.buckets)) {
		for i := range r.buckets {
			r.buckets[i] = bucket{}
		}
		r.epoch = target
		return
	}
	for r.epoch < target {
		r.epoch++
		r.buckets[int(r.epoch)%len(r.buckets)] = bucket{}
	}
}

func (r *ring) add(v int64) {
	r.buckets[int(r.epoch)%len(r.buckets)].value += v
}

// sumWindow sums the trailing window's worth of buckets (capped at ringSize).
func (r *ring) sumWindow(window time.Duration) int64 {
	n := int(window / bucketResolution)
	if n <= 0 {
		n = 1
	}
	if n > len(r.buckets) {
		n = len(r.buckets)
	}
	var total int64
	for i := 0; i < n; i++ {
		idx := int(r.epoch-int64(i)) % len(r.buckets)
		if idx < 0 {
			idx += len(r.buckets)
		}
		total += r.buckets[idx].value
	}
	return total
}

// quotaKey identifies one consumer's counter for one quota type.
type quotaKey struct {
	consumerID string
	quotaType  string
}

// Counter is the in-memory quota sliding-window counter. The zero value is not
// ready to use; call New.
type Counter struct {
	mu    sync.Mutex
	rings map[quotaKey]*ring
	now   func() time.Time // injectable clock for tests
}

// New returns a ready Counter.
func New() *Counter {
	return &Counter{rings: make(map[quotaKey]*ring), now: time.Now}
}

// Record adds amount to consumerID's running total for quotaType (e.g.
// "requests", "tokens"). Window-agnostic: all quotas of this type for this
// consumer, regardless of window, read from the same underlying ring.
func (c *Counter) Record(consumerID, quotaType string, amount int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.ringLocked(consumerID, quotaType)
	now := indexFor(c.now())
	r.advance(now)
	r.add(amount)
}

// Value returns consumerID's quotaType total within the trailing window.
func (c *Counter) Value(consumerID, quotaType string, window time.Duration) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := quotaKey{consumerID, quotaType}
	r, ok := c.rings[key]
	if !ok {
		return 0
	}
	r.advance(indexFor(c.now()))
	return r.sumWindow(window)
}

func (c *Counter) ringLocked(consumerID, quotaType string) *ring {
	key := quotaKey{consumerID, quotaType}
	r, ok := c.rings[key]
	if !ok {
		r = &ring{}
		c.rings[key] = r
	}
	return r
}

// GC drops any counter ring that is empty (over the full maxWindow) at the
// current time. Call periodically to bound memory for consumers that had
// traffic but have since gone idle. Returns the number of rings removed.
func (c *Counter) GC() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := indexFor(c.now())
	var removed int
	for key, r := range c.rings {
		r.advance(now)
		if r.sumWindow(maxWindow) == 0 {
			delete(c.rings, key)
			removed++
		}
	}
	return removed
}

// ParseWindow parses a quota window string ("1m", "1h", "1d", ...) into a
// time.Duration. It extends time.ParseDuration with a "d" (day) suffix, which
// Go's stdlib does not support.
func ParseWindow(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		hours, err := time.ParseDuration(s[:n-1] + "h")
		if err != nil {
			return 0, err
		}
		return hours * 24, nil
	}
	return time.ParseDuration(s)
}
