package storage

import "encoding/json"

// Upstream is a configured upstream provider instance (table: upstreams).
//
// It is the config-schema replacement for the legacy Provider: the vendor
// preset fields (Vendor/PresetKey/Channel/ModelsSource/AuthMode) are dropped,
// the API key becomes CredentialsJSON (a provider-specific credential JSON
// blob), static models become ModelsJSON, and the proxy toggle becomes a
// ProxyURL (empty = no proxy).
//
// Provider is the preset id (or "custom") this upstream was created from; it
// is control-plane metadata used to anchor the UI preset, resolve the model
// discovery fallback URL, and look up the outbound auth scheme — it does not
// participate in data-plane routing. ModelsJSON (static list) and ModelsURL
// (discovery endpoint) are mutually exclusive; discovery results are never
// persisted.
type Upstream struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Provider        string          `json:"provider"`
	Protocol        string          `json:"protocol,omitempty"`
	BaseURL         string          `json:"base_url,omitempty"`
	CredentialsJSON json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      json.RawMessage `json:"models,omitempty"`
	ModelsURL       string          `json:"models_url,omitempty"`
	ProxyURL        string          `json:"proxy_url,omitempty"`
	Enabled         bool            `json:"enabled"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
}

// CreateUpstream is the write DTO for creating an upstream.
type CreateUpstream struct {
	Name            string          `json:"name"`
	Provider        string          `json:"provider"`
	Protocol        string          `json:"protocol,omitempty"`
	BaseURL         string          `json:"base_url,omitempty"`
	CredentialsJSON json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      json.RawMessage `json:"models,omitempty"`
	ModelsURL       string          `json:"models_url,omitempty"`
	ProxyURL        string          `json:"proxy_url,omitempty"`
	Enabled         *bool           `json:"enabled,omitempty"`
}

// UpdateUpstream is the partial-update DTO; nil fields mean "unchanged".
type UpdateUpstream struct {
	Name            *string          `json:"name,omitempty"`
	Provider        *string          `json:"provider,omitempty"`
	Protocol        *string          `json:"protocol,omitempty"`
	BaseURL         *string          `json:"base_url,omitempty"`
	CredentialsJSON *json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      *json.RawMessage `json:"models,omitempty"`
	ModelsURL       *string          `json:"models_url,omitempty"`
	ProxyURL        *string          `json:"proxy_url,omitempty"`
	Enabled         *bool            `json:"enabled,omitempty"`
}

// UpstreamStore is the CRUD store for upstreams under the config-schema model.
// Implementations are added in a later step; this is the contract that
// admin/config-sync/proxy migrate onto.
type UpstreamStore interface {
	List() ([]Upstream, error)
	Get(id string) (*Upstream, error)
	Create(in CreateUpstream) (Upstream, error)
	Update(id string, in UpdateUpstream) (Upstream, error)
	Delete(id string) error
	ExistsByName(name, excludeID string) (bool, error)
}
