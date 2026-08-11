package snapshot

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

// Snapshot is an immutable view of the gateway's configuration at one point
// in time. Readers are safe for concurrent use; the maps are never mutated
// after construction.
type Snapshot struct {
	// upstreams maps upstream ID to Upstream.
	upstreams map[string]storage.Upstream
	// routes maps client-facing Route.Model to Route (Upstreams targets attached).
	routes map[string]storage.Route
	// keysByPreview indexes consumer keys by KeyPreview for FindKey's candidate
	// narrowing (raw tokens are never persisted, so this is the closest to an
	// O(1) lookup available without a per-request DB round trip).
	keysByPreview map[string][]consumerKeyEntry
	// settings holds the gateway-relevant key/value settings (proxy_*).
	settings map[string]string
}

// UpstreamGet returns the upstream with the given ID (nil if absent).
func (s *Snapshot) UpstreamGet(id string) *storage.Upstream {
	u, ok := s.upstreams[id]
	if !ok {
		return nil
	}
	return &u
}

// UpstreamsList returns every upstream (unordered).
func (s *Snapshot) UpstreamsList() []storage.Upstream {
	out := make([]storage.Upstream, 0, len(s.upstreams))
	for _, u := range s.upstreams {
		out = append(out, u)
	}
	return out
}

// RouteByModel returns the route registered under model (nil if absent).
func (s *Snapshot) RouteByModel(model string) *storage.Route {
	r, ok := s.routes[model]
	if !ok {
		return nil
	}
	return &r
}

// RoutesList returns every route, with targets attached.
func (s *Snapshot) RoutesList() []storage.Route {
	out := make([]storage.Route, 0, len(s.routes))
	for _, r := range s.routes {
		out = append(out, r)
	}
	return out
}

// FindKey resolves a raw consumer-key token to its access record: filter
// candidates by preview, then compare hashes (raw tokens are never persisted,
// so an exact-match map lookup like the legacy FindAPIKey isn't possible).
func (s *Snapshot) FindKey(rawKey string) *storage.ConsumerKeyAccessRecord {
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
func (s *Snapshot) SettingGet(key string) (string, bool) {
	v, ok := s.settings[key]
	return v, ok
}

// Builder constructs a Snapshot incrementally. Build freezes the accumulated
// resources into an immutable read model. Maps are lazily allocated so callers
// can set only the sections they have.
type Builder struct {
	upstreams map[string]storage.Upstream
	routes    map[string]storage.Route
	keys      []consumerKeyEntry
	settings  map[string]string
}

// SetUpstream adds (or replaces) an upstream keyed by ID.
func (b *Builder) SetUpstream(u storage.Upstream) {
	if b.upstreams == nil {
		b.upstreams = map[string]storage.Upstream{}
	}
	b.upstreams[u.ID] = u
}

// SetRoute adds (or replaces) a route keyed by Model.
func (b *Builder) SetRoute(r storage.Route) {
	if b.routes == nil {
		b.routes = map[string]storage.Route{}
	}
	b.routes[r.Model] = r
}

// AddConsumerKey registers one consumer key's gateway-facing view (preview,
// hash, grants). Called once per key across all consumers.
func (b *Builder) AddConsumerKey(keyID, consumerID, name, keyPreview, keyHash string, enabled bool, expiresAt string, routes []string, quotas []storage.ConsumerQuota) {
	b.keys = append(b.keys, consumerKeyEntry{
		KeyID: keyID, ConsumerID: consumerID, Name: name, KeyPreview: keyPreview, KeyHash: keyHash,
		Enabled: enabled, ExpiresAt: expiresAt, Routes: routes, Quotas: quotas,
	})
}

// SetSetting adds (or replaces) a setting.
func (b *Builder) SetSetting(key, value string) {
	if b.settings == nil {
		b.settings = map[string]string{}
	}
	b.settings[key] = value
}

// Build freezes the builder into an immutable Snapshot.
func (b *Builder) Build() *Snapshot {
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
	for _, key := range b.keys {
		byPreview[key.KeyPreview] = append(byPreview[key.KeyPreview], key)
	}
	return &Snapshot{
		upstreams:     b.upstreams,
		routes:        b.routes,
		keysByPreview: byPreview,
		settings:      b.settings,
	}
}
