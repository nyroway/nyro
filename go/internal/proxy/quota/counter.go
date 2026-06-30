// Package quota is the in-memory sliding-window counter for API-key rate/token
// quotas (RPM/RPD/TPM/TPD). It replaces the request_logs scan that the inbound
// access check used to perform on every authenticated request.
//
// The counter is per-gateway-process and in-memory: a restart loses all counts.
// Quotas are soft limits (they bound concurrent usage, not correctness), so this
// is acceptable — the worst case after a restart is a brief over-admission until
// the windows refill.
//
// Design: each API key has two sliding windows.
//   - Minute window (RPM/TPM): a ring of 60 one-second buckets.
//   - Day window (RPD/TPD): a ring of 24 one-hour buckets.
//
// Reads sum the buckets whose [start, end) overlaps the trailing N units of time
// (60 seconds / 24 hours). Writes lazily drop buckets older than the window
// before adding, so the map can never grow unbounded for active keys; idle keys
// are pruned on read and via a periodic GC sweep. Thread-safe (single mutex).
package quota

import (
	"sync"
	"time"
)

// Window selects a quota window.
type Window int

const (
	WindowMinute Window = iota // trailing 60 seconds
	WindowDay                  // trailing 24 hours
)

const (
	minuteBuckets = 60 // one per second
	dayBuckets    = 24 // one per hour
)

// bucket holds request/token totals for one time slot.
type bucket struct {
	request int64
	tokens  int64
}

type ring struct {
	// epoch is the absolute index of the oldest in-window bucket (seconds for
	// the minute window, hours for the day window). It advances on write so
	// stale buckets can be zeroed in place rather than deleted.
	epoch   int64
	buckets []bucket
}

func newMinuteRing() *ring {
	return &ring{buckets: make([]bucket, minuteBuckets)}
}

func newDayRing() *ring {
	return &ring{buckets: make([]bucket, dayBuckets)}
}

// advance zeroes any buckets that fall out of the [target-len+1, target] window
// and moves the ring's epoch forward to target. If more than len(ring) steps
// elapsed, the whole ring is stale and is cleared. Called under the counter
// mutex.
func (r *ring) advance(target int64) {
	if target <= r.epoch {
		// Clock moved back or held steady: nothing to clear. (Time is monotonic
		// in practice; this just guards against jitter in injected test clocks.)
		return
	}
	if target-r.epoch >= int64(len(r.buckets)) {
		// A full ring (or more) elapsed since the last touch: every bucket is
		// stale. Clear all and jump.
		for i := range r.buckets {
			r.buckets[i] = bucket{}
		}
		r.epoch = target
		return
	}
	for r.epoch < target {
		r.epoch++
		b := &r.buckets[int(r.epoch)%len(r.buckets)]
		b.request = 0
		b.tokens = 0
	}
}

// add increments the current bucket by (requests, tokens).
func (r *ring) add(request, tokens int64) {
	b := &r.buckets[int(r.epoch)%len(r.buckets)]
	b.request += request
	b.tokens += tokens
}

// sum returns the total requests/tokens across the whole ring. advance keeps
// the ring holding only in-window buckets, so a full sum is the window sum.
func (r *ring) sum() (requests, tokens int64) {
	for i := range r.buckets {
		requests += r.buckets[i].request
		tokens += r.buckets[i].tokens
	}
	return requests, tokens
}

// keyState is the per-API-key counter state.
type keyState struct {
	minute *ring
	day    *ring
}

// Counter is the in-memory quota sliding-window counter. The zero value is a
// ready, empty counter.
type Counter struct {
	mu    sync.Mutex
	state map[string]*keyState
	now   func() time.Time // injectable clock for tests
}

// New returns a ready Counter.
func New() *Counter {
	return &Counter{state: make(map[string]*keyState), now: time.Now}
}

// Record adds requests/tokens for apiKeyID to both windows. A request that fails
// before token usage is known should call Record(id, 1, 0).
func (c *Counter) Record(apiKeyID string, requests, tokens int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordLocked(apiKeyID, requests, tokens)
}

func (c *Counter) recordLocked(apiKeyID string, requests, tokens int64) {
	ks := c.state[apiKeyID]
	if ks == nil {
		ks = &keyState{minute: newMinuteRing(), day: newDayRing()}
		c.state[apiKeyID] = ks
	}
	now := c.now()
	ks.minute.advance(now.Unix())
	ks.day.advance(now.Unix() / 3600)
	ks.minute.add(requests, tokens)
	ks.day.add(requests, tokens)
}

// Requests returns the request count for apiKeyID in window w.
func (c *Counter) Requests(apiKeyID string, w Window) int64 {
	n, _ := c.sums(apiKeyID, w)
	return n
}

// Tokens returns the token count for apiKeyID in window w.
func (c *Counter) Tokens(apiKeyID string, w Window) int64 {
	_, n := c.sums(apiKeyID, w)
	return n
}

func (c *Counter) sums(apiKeyID string, w Window) (requests, tokens int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ks := c.state[apiKeyID]
	if ks == nil {
		return 0, 0
	}
	// Advance without adding so stale buckets are dropped before summing, and so
	// the sum reflects the trailing window at read time.
	now := c.now()
	switch w {
	case WindowMinute:
		ks.minute.advance(now.Unix())
		return ks.minute.sum()
	case WindowDay:
		ks.day.advance(now.Unix() / 3600)
		return ks.day.sum()
	}
	return 0, 0
}

// GC drops any API key whose both windows are empty at the current time. It
// advances each key's rings to "now" first so fully-expired entries are detected.
// Call periodically to bound memory for keys that had traffic but have since
// gone idle. Returns the number of keys removed.
func (c *Counter) GC() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	sec := now.Unix()
	hr := now.Unix() / 3600
	var removed int
	for id, ks := range c.state {
		ks.minute.advance(sec)
		ks.day.advance(hr)
		mr, mt := ks.minute.sum()
		dr, dt := ks.day.sum()
		if mr == 0 && mt == 0 && dr == 0 && dt == 0 {
			delete(c.state, id)
			removed++
		}
	}
	return removed
}
