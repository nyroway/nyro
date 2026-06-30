package xds

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
// transitional DB-backed populator: providers, models (with targets), API keys
// + bindings, all settings, and OAuth credentials (P3b moved OAuth into the
// cache so the gateway refreshes locally instead of via DB CAS).
//
// Models().List() already returns each model with its targets attached, so no
// separate ModelBackends() call is needed; APIKeys().List() carries each key's
// token and bound model IDs, which is the richer source for the auth read model.
func LoadFromStorage(s storage.Storage) (*ConfigSnapshot, error) {
	snap := &ConfigSnapshot{
		providers: map[string]storage.Provider{},
		models:    map[string]storage.Model{},
		apikeys:   map[string]storage.ApiKeyAccessRecord{},
		bindings:  map[string]map[string]bool{},
		settings:  map[string]string{},
		oauth:     map[string]storage.OAuthCredential{},
	}

	providers, err := s.Providers().List()
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		snap.providers[p.ID] = p
	}

	models, err := s.Models().List()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		// Copy to avoid aliasing the storage-owned slice header in the
		// returned Model (the map stores a value copy, but Targets is a slice).
		cp := m
		if len(m.Targets) > 0 {
			cp.Targets = append([]storage.ModelBackend(nil), m.Targets...)
		}
		snap.models[m.Name] = cp
	}

	keys, err := s.APIKeys().List()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.Token == "" {
			continue
		}
		snap.apikeys[k.Token] = storage.ApiKeyAccessRecord{
			ID: k.ID, Name: k.Name, IsEnabled: k.IsEnabled, ExpiresAt: k.ExpiresAt,
			RPM: k.RPM, RPD: k.RPD, TPM: k.TPM, TPD: k.TPD,
		}
		set := map[string]bool{}
		for _, mid := range k.ModelIDs {
			set[mid] = true
		}
		snap.bindings[k.ID] = set
	}

	settings, err := s.Settings().ListAll()
	if err != nil {
		return nil, err
	}
	for _, kv := range settings {
		snap.settings[kv.Key] = kv.Value
	}

	creds, err := s.OAuthCredentials().ListAll()
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		snap.oauth[c.ProviderID] = c
	}

	return snap, nil
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
