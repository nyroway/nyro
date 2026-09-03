// Package snapshot owns immutable configuration views and their atomic publication cache.
package snapshot

// Snapshot is an immutable view of the gateway's configuration at one point
// in time. Readers are safe for concurrent use; its maps are never mutated
// after construction.
type Snapshot struct {
	upstreams     map[string]Upstream
	routes        map[string]Route
	keysByPreview map[string][]consumerKeyEntry
	settings      map[string]string
}

// UpstreamGet returns a copy of the upstream with the given ID (nil if absent).
func (s *Snapshot) UpstreamGet(id string) *Upstream {
	upstream, ok := s.upstreams[id]
	if !ok {
		return nil
	}
	upstream = cloneUpstream(upstream)
	return &upstream
}

// UpstreamsList returns copies of every upstream (unordered).
func (s *Snapshot) UpstreamsList() []Upstream {
	out := make([]Upstream, 0, len(s.upstreams))
	for _, upstream := range s.upstreams {
		out = append(out, cloneUpstream(upstream))
	}
	return out
}

// RouteByModel returns a copy of the route registered under model (nil if absent).
func (s *Snapshot) RouteByModel(model string) *Route {
	route, ok := s.routes[model]
	if !ok {
		return nil
	}
	route = cloneRoute(route)
	return &route
}

// RoutesList returns copies of every route, with targets attached.
func (s *Snapshot) RoutesList() []Route {
	out := make([]Route, 0, len(s.routes))
	for _, route := range s.routes {
		out = append(out, cloneRoute(route))
	}
	return out
}

// FindKey resolves a raw consumer-key token to its access record: filter
// candidates by preview, then compare hashes. Raw tokens are never retained.
func (s *Snapshot) FindKey(rawKey string) *ConsumerAccess {
	preview := previewOf(rawKey)
	hash := hashKey(rawKey)
	for _, entry := range s.keysByPreview[preview] {
		if entry.keyHash == hash {
			access := cloneConsumerAccess(entry.ConsumerAccess)
			return &access
		}
	}
	return nil
}

// SettingGet returns the value for key ("", false if absent).
func (s *Snapshot) SettingGet(key string) (string, bool) {
	value, ok := s.settings[key]
	return value, ok
}

// Builder constructs a Snapshot incrementally. Build freezes the accumulated
// resources into an immutable read model. Maps are lazily allocated so callers
// can set only the sections they have.
type Builder struct {
	upstreams map[string]Upstream
	routes    map[string]Route
	keys      []consumerKeyEntry
	settings  map[string]string
}

// SetUpstream adds (or replaces) an upstream keyed by ID.
func (b *Builder) SetUpstream(upstream Upstream) {
	if b.upstreams == nil {
		b.upstreams = map[string]Upstream{}
	}
	b.upstreams[upstream.ID] = cloneUpstream(upstream)
}

// SetRoute adds (or replaces) a route keyed by Model.
func (b *Builder) SetRoute(route Route) {
	if b.routes == nil {
		b.routes = map[string]Route{}
	}
	b.routes[route.Model] = cloneRoute(route)
}

// AddConsumerKey registers one consumer key's gateway-facing view (preview,
// hash, grants). Called once per key across all consumers.
func (b *Builder) AddConsumerKey(keyID, consumerID, name, keyPreview, keyHash string, enabled bool, expiresAt string, routes []string, quotas []ConsumerQuota) {
	b.keys = append(b.keys, consumerKeyEntry{
		ConsumerAccess: cloneConsumerAccess(ConsumerAccess{
			KeyID: keyID, ConsumerID: consumerID, Name: name, KeyPreview: keyPreview,
			Enabled: enabled, ExpiresAt: expiresAt, Routes: routes, Quotas: quotas,
		}),
		keyHash: keyHash,
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
	upstreams := make(map[string]Upstream, len(b.upstreams))
	for id, upstream := range b.upstreams {
		upstreams[id] = cloneUpstream(upstream)
	}
	routes := make(map[string]Route, len(b.routes))
	for model, route := range b.routes {
		routes[model] = cloneRoute(route)
	}
	settings := make(map[string]string, len(b.settings))
	for key, value := range b.settings {
		settings[key] = value
	}
	byPreview := make(map[string][]consumerKeyEntry, len(b.keys))
	for _, entry := range b.keys {
		entry.ConsumerAccess = cloneConsumerAccess(entry.ConsumerAccess)
		byPreview[entry.KeyPreview] = append(byPreview[entry.KeyPreview], entry)
	}
	return &Snapshot{upstreams: upstreams, routes: routes, keysByPreview: byPreview, settings: settings}
}
