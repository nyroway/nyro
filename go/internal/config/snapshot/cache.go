package snapshot

import (
	"sync/atomic"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
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

// LoadFromStorage builds a snapshot by querying storage once: upstreams,
// routes (with targets), consumers (with keys, route grants, and quotas), and
// all settings. Raw key tokens are never loaded.
func LoadFromStorage(s storage.Storage) (*Snapshot, error) {
	builder := &Builder{}

	upstreams, err := s.Upstreams().List()
	if err != nil {
		return nil, err
	}
	for _, upstream := range upstreams {
		builder.SetUpstream(upstream)
	}

	routes, err := s.Routes().List()
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		builder.SetRoute(route)
	}

	consumers, err := s.Consumers().List()
	if err != nil {
		return nil, err
	}
	for _, consumer := range consumers {
		for _, key := range consumer.Keys {
			// A key is only usable when both it and its owning consumer are
			// enabled; disabling a consumer revokes every key it owns.
			builder.AddConsumerKey(key.ID, consumer.ID, key.Name, key.KeyPreview, key.KeyHash, key.Enabled && consumer.Enabled, key.ExpiresAt, consumer.Routes, consumer.Quotas)
		}
	}

	settings, err := s.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, setting := range settings {
		builder.SetSetting(setting.Key, setting.Value)
	}

	return builder.Build(), nil
}

// LoadAndSwap loads a snapshot from storage and publishes it to the cache.
// Returns the load error without swapping on failure.
func (c *Cache) LoadAndSwap(s storage.Storage) error {
	snap, err := LoadFromStorage(s)
	if err != nil {
		return err
	}
	c.Swap(snap)
	return nil
}

// StartLoaderLoop runs a background refresh: an immediate load, then a
// periodic reload at interval until the returned stop function is called.
func (c *Cache) StartLoaderLoop(s storage.Storage, interval time.Duration, errCh chan<- error) (stop func()) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	if err := c.LoadAndSwap(s); err != nil {
		sendErr(errCh, err)
	}
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.LoadAndSwap(s); err != nil {
					sendErr(errCh, err)
				}
			case <-stopCh:
				return
			}
		}
	}()
	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(stopCh)
		<-done
	}
}

// sendErr delivers err to ch without blocking; drops it if ch is full.
func sendErr(ch chan<- error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}
