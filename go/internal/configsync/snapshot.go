package configsync

import "github.com/nyroway/nyro/go/internal/storage"

// consumerKeyEntry is the gateway-facing view of a consumer key: enough to
// authenticate a raw token locally (preview filter + hash compare) plus the
// grants needed to answer FindKey in one shot. It never carries the plaintext
// token.
type consumerKeyEntry struct {
	KeyID      string
	ConsumerID string
	Name       string
	KeyPreview string
	KeyHash    string
	Enabled    bool
	ExpiresAt  string
	Routes     []string
	Quotas     []storage.ConsumerQuota
}

// ConfigSnapshot is an immutable view of the gateway's configuration at one
// point in time. Readers are safe for concurrent use; the maps are never
// mutated after construction.
type ConfigSnapshot struct {
	// upstreams maps upstream ID → Upstream.
	upstreams map[string]storage.Upstream
	// routes maps client-facing Route.Model → Route (Upstreams targets attached).
	routes map[string]storage.Route
	// keysByPreview indexes consumer keys by KeyPreview for FindKey's candidate
	// narrowing (raw tokens are never persisted, so this is the closest to an
	// O(1) lookup available without a per-request DB round trip).
	keysByPreview map[string][]consumerKeyEntry
	// settings holds the gateway-relevant key/value settings (proxy_*).
	settings map[string]string
}

// UpstreamGet returns the upstream with the given ID (nil if absent).
func (s *ConfigSnapshot) UpstreamGet(id string) *storage.Upstream {
	u, ok := s.upstreams[id]
	if !ok {
		return nil
	}
	return &u
}

// UpstreamsList returns every upstream (unordered).
func (s *ConfigSnapshot) UpstreamsList() []storage.Upstream {
	out := make([]storage.Upstream, 0, len(s.upstreams))
	for _, u := range s.upstreams {
		out = append(out, u)
	}
	return out
}

// RouteByModel returns the route registered under model (nil if absent).
func (s *ConfigSnapshot) RouteByModel(model string) *storage.Route {
	r, ok := s.routes[model]
	if !ok {
		return nil
	}
	return &r
}

// RoutesList returns every route, with targets attached.
func (s *ConfigSnapshot) RoutesList() []storage.Route {
	out := make([]storage.Route, 0, len(s.routes))
	for _, r := range s.routes {
		out = append(out, r)
	}
	return out
}

// FindKey resolves a raw consumer-key token to its access record: filter
// candidates by preview, then compare hashes (raw tokens are never persisted,
// so an exact-match map lookup like the legacy FindAPIKey isn't possible).
func (s *ConfigSnapshot) FindKey(rawKey string) *storage.ConsumerKeyAccessRecord {
	preview := storage.PreviewOf(rawKey)
	hash := storage.HashKey(rawKey)
	for _, entry := range s.keysByPreview[preview] {
		if entry.KeyHash != hash {
			continue
		}
		return &storage.ConsumerKeyAccessRecord{
			KeyID:      entry.KeyID,
			ConsumerID: entry.ConsumerID,
			Name:       entry.Name,
			KeyPreview: entry.KeyPreview,
			Enabled:    entry.Enabled,
			ExpiresAt:  entry.ExpiresAt,
			Routes:     entry.Routes,
			Quotas:     entry.Quotas,
		}
	}
	return nil
}

// SettingGet returns the value for key ("", false if absent).
func (s *ConfigSnapshot) SettingGet(key string) (string, bool) {
	v, ok := s.settings[key]
	return v, ok
}

// Snapshot is a build helper for constructing a ConfigSnapshot incrementally
// (used by standalone YAML config). Call Done to freeze it into a ConfigSnapshot.
// Maps are lazily allocated so callers can set only the sections they have.
type Snapshot struct {
	upstreams map[string]storage.Upstream
	routes    map[string]storage.Route
	keys      []consumerKeyEntry
	settings  map[string]string
}

// SetUpstream adds (or replaces) an upstream keyed by ID.
func (b *Snapshot) SetUpstream(u storage.Upstream) {
	if b.upstreams == nil {
		b.upstreams = map[string]storage.Upstream{}
	}
	b.upstreams[u.ID] = u
}

// SetRoute adds (or replaces) a route keyed by Model.
func (b *Snapshot) SetRoute(r storage.Route) {
	if b.routes == nil {
		b.routes = map[string]storage.Route{}
	}
	b.routes[r.Model] = r
}

// AddConsumerKey registers one consumer key's gateway-facing view (preview,
// hash, grants). Called once per key across all consumers.
func (b *Snapshot) AddConsumerKey(keyID, consumerID, name, keyPreview, keyHash string, enabled bool, expiresAt string, routes []string, quotas []storage.ConsumerQuota) {
	b.keys = append(b.keys, consumerKeyEntry{
		KeyID: keyID, ConsumerID: consumerID, Name: name, KeyPreview: keyPreview, KeyHash: keyHash,
		Enabled: enabled, ExpiresAt: expiresAt, Routes: routes, Quotas: quotas,
	})
}

// SetSetting adds (or replaces) a setting.
func (b *Snapshot) SetSetting(key, value string) {
	if b.settings == nil {
		b.settings = map[string]string{}
	}
	b.settings[key] = value
}

// Done freezes the builder into an immutable ConfigSnapshot.
func (b *Snapshot) Done() *ConfigSnapshot {
	if b.upstreams == nil {
		b.upstreams = map[string]storage.Upstream{}
	}
	if b.routes == nil {
		b.routes = map[string]storage.Route{}
	}
	if b.settings == nil {
		b.settings = map[string]string{}
	}
	byPreview := make(map[string][]consumerKeyEntry, len(b.keys))
	for _, k := range b.keys {
		byPreview[k.KeyPreview] = append(byPreview[k.KeyPreview], k)
	}
	return &ConfigSnapshot{
		upstreams:     b.upstreams,
		routes:        b.routes,
		keysByPreview: byPreview,
		settings:      b.settings,
	}
}
