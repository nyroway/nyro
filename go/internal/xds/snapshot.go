// Package xds holds the gateway's in-memory configuration cache (the read side
// of the xDS control loop). In Phase 1 the cache is populated from the DB by a
// transitional loader and serves the gateway's config reads (models, providers,
// API keys, bindings, proxy settings). OAuth credentials and request-log quota
// counters stay on storage for now; they migrate in later phases.
package xds

import "github.com/nyroway/nyro/go/internal/storage"

// ConfigSnapshot is an immutable view of the gateway's configuration at one
// point in time. Readers are safe for concurrent use; the maps are never
// mutated after construction.
type ConfigSnapshot struct {
	// providers maps provider ID → Provider.
	providers map[string]storage.Provider
	// models maps client-facing model Name → Model (Targets attached).
	models map[string]storage.Model
	// apikeys maps raw API-key token → access record (the read model used by
	// the inbound auth check).
	apikeys map[string]storage.ApiKeyAccessRecord
	// bindings maps apiKey ID → set of bound model IDs.
	bindings map[string]map[string]bool
	// settings holds the gateway-relevant key/value settings (proxy_*).
	settings map[string]string
	// oauth maps provider ID → stored OAuth credential. Populated by the xDS
	// snapshot (Phase 3b); the gateway refreshes these locally under a
	// per-process mutex instead of cross-replica DB CAS.
	oauth map[string]storage.OAuthCredential
}

// ModelByName returns the model registered under name (nil if absent).
func (s *ConfigSnapshot) ModelByName(name string) *storage.Model {
	m, ok := s.models[name]
	if !ok {
		return nil
	}
	return &m
}

// ModelsList returns every model, sorted by name (matching storage's ordering).
func (s *ConfigSnapshot) ModelsList() []storage.Model {
	out := make([]storage.Model, 0, len(s.models))
	for _, m := range s.models {
		out = append(out, m)
	}
	return out
}

// ProviderGet returns the provider with the given ID (nil if absent).
func (s *ConfigSnapshot) ProviderGet(id string) *storage.Provider {
	p, ok := s.providers[id]
	if !ok {
		return nil
	}
	return &p
}

// FindAPIKey returns the access record for a raw token (nil if absent).
func (s *ConfigSnapshot) FindAPIKey(raw string) *storage.ApiKeyAccessRecord {
	rec, ok := s.apikeys[raw]
	if !ok {
		return nil
	}
	return &rec
}

// ModelBindingExists reports whether apiKeyID is bound to modelID.
func (s *ConfigSnapshot) ModelBindingExists(apiKeyID, modelID string) bool {
	if set, ok := s.bindings[apiKeyID]; ok {
		return set[modelID]
	}
	return false
}

// ListBoundModelIDs returns the model IDs bound to apiKeyID (empty if none).
func (s *ConfigSnapshot) ListBoundModelIDs(apiKeyID string) []string {
	set := s.bindings[apiKeyID]
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// SettingGet returns the value for key ("", false if absent).
func (s *ConfigSnapshot) SettingGet(key string) (string, bool) {
	v, ok := s.settings[key]
	return v, ok
}

// OAuthGet returns the stored OAuth credential for providerID (nil if absent).
// This is the cache read path for the gateway's OAuth resolve + refresh (P3b);
// it replaces the former storage.OAuthCredentials().Get read.
func (s *ConfigSnapshot) OAuthGet(providerID string) *storage.OAuthCredential {
	c, ok := s.oauth[providerID]
	if !ok {
		return nil
	}
	return &c
}

// OAuthList returns every stored OAuth credential. Used by the background
// refresh loop to find tokens expiring within its horizon.
func (s *ConfigSnapshot) OAuthList() []storage.OAuthCredential {
	out := make([]storage.OAuthCredential, 0, len(s.oauth))
	for _, c := range s.oauth {
		out = append(out, c)
	}
	return out
}

// Snapshot is a build helper for constructing a ConfigSnapshot incrementally
// (used by standalone YAML config). Call Done to freeze it into a ConfigSnapshot.
// Maps are lazily allocated so callers can set only the sections they have.
type Snapshot struct {
	providers map[string]storage.Provider
	models    map[string]storage.Model
	apikeys   map[string]storage.ApiKeyAccessRecord
	bindings  map[string]map[string]bool
	settings  map[string]string
	oauth     map[string]storage.OAuthCredential
}

// SetProvider adds (or replaces) a provider keyed by ID.
func (b *Snapshot) SetProvider(p storage.Provider) {
	if b.providers == nil {
		b.providers = map[string]storage.Provider{}
	}
	b.providers[p.ID] = p
}

// SetModel adds (or replaces) a model keyed by Name.
func (b *Snapshot) SetModel(m storage.Model) {
	if b.models == nil {
		b.models = map[string]storage.Model{}
	}
	b.models[m.Name] = m
}

// SetAPIKey adds (or replaces) an API-key access record keyed by token, and
// registers its bindings (apiKeyID → set of model IDs).
func (b *Snapshot) SetAPIKey(token string, rec storage.ApiKeyAccessRecord, boundModelIDs map[string]bool) {
	if b.apikeys == nil {
		b.apikeys = map[string]storage.ApiKeyAccessRecord{}
	}
	if b.bindings == nil {
		b.bindings = map[string]map[string]bool{}
	}
	b.apikeys[token] = rec
	b.bindings[rec.ID] = boundModelIDs
}

// SetSetting adds (or replaces) a setting.
func (b *Snapshot) SetSetting(key, value string) {
	if b.settings == nil {
		b.settings = map[string]string{}
	}
	b.settings[key] = value
}

// SetOAuth adds (or replaces) an OAuth credential keyed by provider ID.
func (b *Snapshot) SetOAuth(c storage.OAuthCredential) {
	if b.oauth == nil {
		b.oauth = map[string]storage.OAuthCredential{}
	}
	b.oauth[c.ProviderID] = c
}

// Done freezes the builder into an immutable ConfigSnapshot.
func (b *Snapshot) Done() *ConfigSnapshot {
	if b.providers == nil {
		b.providers = map[string]storage.Provider{}
	}
	if b.models == nil {
		b.models = map[string]storage.Model{}
	}
	if b.apikeys == nil {
		b.apikeys = map[string]storage.ApiKeyAccessRecord{}
	}
	if b.bindings == nil {
		b.bindings = map[string]map[string]bool{}
	}
	if b.settings == nil {
		b.settings = map[string]string{}
	}
	if b.oauth == nil {
		b.oauth = map[string]storage.OAuthCredential{}
	}
	return &ConfigSnapshot{
		providers: b.providers,
		models:    b.models,
		apikeys:   b.apikeys,
		bindings:  b.bindings,
		settings:  b.settings,
		oauth:     b.oauth,
	}
}
