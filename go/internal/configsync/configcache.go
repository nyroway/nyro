package configsync

import (
	"sync/atomic"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// ConfigCache is the gateway's atomic config store. Readers call Load() to get
// the current snapshot (lock-free); the loader calls Swap() to publish a fresh
// one. Until the first Swap, Load() returns nil and Ready() is false.
type ConfigCache struct {
	snap atomic.Pointer[ConfigSnapshot]
}

// Load returns the current snapshot (nil before the first Swap).
func (c *ConfigCache) Load() *ConfigSnapshot { return c.snap.Load() }

// Swap atomically publishes a new snapshot.
func (c *ConfigCache) Swap(s *ConfigSnapshot) { c.snap.Store(s) }

// Ready reports whether a snapshot has been published.
func (c *ConfigCache) Ready() bool { return c.snap.Load() != nil }

// LoadFromStorage builds a snapshot by querying storage once. It is the
// transitional DB-backed populator: upstreams, routes (with targets), consumers
// (with keys — prefix+hash only, never plaintext — route grants, and quotas),
// and all settings.
func LoadFromStorage(s storage.Storage) (*ConfigSnapshot, error) {
	b := &Snapshot{}

	upstreams, err := s.Upstreams().List()
	if err != nil {
		return nil, err
	}
	for _, u := range upstreams {
		b.SetUpstream(u)
	}

	routes, err := s.Routes().List()
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		b.SetRoute(r)
	}

	consumers, err := s.Consumers().List()
	if err != nil {
		return nil, err
	}
	for _, c := range consumers {
		for _, k := range c.Keys {
			// A key is only usable when both it and its owning consumer are
			// enabled — disabling a consumer must revoke every key it owns.
			b.AddConsumerKey(k.ID, c.ID, k.KeyPreview, k.KeyHash, k.Enabled && c.Enabled, k.ExpiresAt, c.Routes, c.Quotas)
		}
	}

	settings, err := s.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, kv := range settings {
		b.SetSetting(kv.Key, kv.Value)
	}

	return b.Done(), nil
}

// LoadAndSwap loads a snapshot from storage and publishes it to the cache.
// Returns the load error without swapping on failure.
func (c *ConfigCache) LoadAndSwap(s storage.Storage) error {
	snap, err := LoadFromStorage(s)
	if err != nil {
		return err
	}
	c.Swap(snap)
	return nil
}

// StartLoaderLoop runs a background refresh: an immediate load, then a periodic
// reload at interval until the returned stop function is called. Load errors
// are non-fatal (the previous snapshot keeps serving); each error is sent to
// errCh non-blockingly when non-nil. stop halts the ticker and waits for the
// loop goroutine to exit (safe to call multiple times).
func (c *ConfigCache) StartLoaderLoop(s storage.Storage, interval time.Duration, errCh chan<- error) (stop func()) {
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
