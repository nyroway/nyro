package snapshot

import (
	"sync/atomic"
)

// Cache is the gateway's atomic config store. Readers call Load to get the
// current snapshot (lock-free); configuration sources call Swap to publish a
// fresh one. Until the first Swap, Load returns nil and Ready is false.
type Cache struct {
	snap atomic.Pointer[Snapshot]
}

// Load returns the current snapshot (nil before the first Swap).
func (c *Cache) Load() *Snapshot { return c.snap.Load() }

// Swap atomically publishes a new snapshot.
func (c *Cache) Swap(s *Snapshot) {
	c.snap.Store(s)
}

// Ready reports whether a snapshot has been published.
func (c *Cache) Ready() bool { return c.snap.Load() != nil }
