package snapshot

import (
	"sync/atomic"
)

// Cache is the gateway's atomic config store. Readers call Load to get the
// current snapshot (lock-free); configuration sources call Swap to publish a
// fresh one. Until the first Swap, Load returns nil and Ready is false.
type Cache struct {
	snap atomic.Pointer[Snapshot]
	// onSwap is an optional callback fired after every Swap publishes a new
	// snapshot. It lets runtime composition re-resolve dependent state after a
	// config update. Stored behind an atomic pointer so SetOnSwap is safe to call
	// concurrently with Swap.
	onSwap atomic.Pointer[func()]
}

// Load returns the current snapshot (nil before the first Swap).
func (c *Cache) Load() *Snapshot { return c.snap.Load() }

// SetOnSwap registers (or replaces, when called again) the callback invoked
// after each Swap. Pass nil to clear it.
func (c *Cache) SetOnSwap(fn func()) {
	if fn == nil {
		c.onSwap.Store(nil)
		return
	}
	c.onSwap.Store(&fn)
}

// Swap atomically publishes a new snapshot, then fires the onSwap callback (if
// any). The callback runs synchronously after the snapshot is visible.
func (c *Cache) Swap(s *Snapshot) {
	c.snap.Store(s)
	if fn := c.onSwap.Load(); fn != nil {
		(*fn)()
	}
}

// Ready reports whether a snapshot has been published.
func (c *Cache) Ready() bool { return c.snap.Load() != nil }
