package storage

import "encoding/json"

// Upstream is a configured upstream provider instance (table: upstreams).
//
// It is the config-schema replacement for the legacy Provider: the vendor
// preset fields (Vendor/PresetKey/Channel/ModelsSource/AuthMode) are dropped,
// the API key becomes CredentialsJSON (a provider-specific credential JSON
// blob), static models become ModelsJSON, and the proxy toggle becomes a
// ProxyURL (empty = no proxy).
type Upstream struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Protocol        string          `json:"protocol,omitempty"`
	BaseURL         string          `json:"base_url,omitempty"`
	CredentialsJSON json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      json.RawMessage `json:"models,omitempty"`
	ProxyURL        string          `json:"proxy_url,omitempty"`
	Enabled         bool            `json:"enabled"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
}

// CreateUpstream is the write DTO for creating an upstream.
type CreateUpstream struct {
	Name            string          `json:"name"`
	Protocol        string          `json:"protocol,omitempty"`
	BaseURL         string          `json:"base_url,omitempty"`
	CredentialsJSON json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      json.RawMessage `json:"models,omitempty"`
	ProxyURL        string          `json:"proxy_url,omitempty"`
	Enabled         *bool           `json:"enabled,omitempty"`
}

// UpdateUpstream is the partial-update DTO; nil fields mean "unchanged".
type UpdateUpstream struct {
	Name            *string          `json:"name,omitempty"`
	Protocol        *string          `json:"protocol,omitempty"`
	BaseURL         *string          `json:"base_url,omitempty"`
	CredentialsJSON *json.RawMessage `json:"credentials,omitempty"`
	ModelsJSON      *json.RawMessage `json:"models,omitempty"`
	ProxyURL        *string          `json:"proxy_url,omitempty"`
	Enabled         *bool            `json:"enabled,omitempty"`
}

// UpstreamStore is the CRUD store for upstreams under the config-schema model.
// Implementations are added in a later step; this is the contract that
// admin/xDS/proxy migrate onto.
type UpstreamStore interface {
	List() ([]Upstream, error)
	Get(id string) (*Upstream, error)
	Create(in CreateUpstream) (Upstream, error)
	Update(id string, in UpdateUpstream) (Upstream, error)
	Delete(id string) error
	ExistsByName(name, excludeID string) (bool, error)
}
