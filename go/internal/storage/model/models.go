// Package model contains canonical GORM database entities for the SQL storage
// backend. These structs are the input source for gorm/gen.
package model

// Upstream is one configured upstream provider instance.
type Upstream struct {
	ID              string `gorm:"column:id;primaryKey"`
	Name            string `gorm:"column:name;uniqueIndex;not null;size:255"`
	Provider        string `gorm:"column:provider;not null;default:custom"`
	Protocol        string `gorm:"column:protocol"`
	BaseURL         string `gorm:"column:base_url"`
	CredentialsJSON string `gorm:"column:credentials_json"`
	ModelsJSON      string `gorm:"column:models_json"`
	ModelsURL       string `gorm:"column:models_url"`
	ProxyURL        string `gorm:"column:proxy_url"`
	Enabled         bool   `gorm:"column:enabled;not null;default:true"`
	CreatedAt       string `gorm:"column:created_at;not null"`
	UpdatedAt       string `gorm:"column:updated_at;not null"`
}

func (Upstream) TableName() string { return "upstreams" }

// Route is one client-visible model route.
type Route struct {
	ID            string `gorm:"column:id;primaryKey"`
	Model         string `gorm:"column:model;uniqueIndex;not null;size:255"`
	Balance       string `gorm:"column:balance;not null;default:weighted"`
	EnableAuth    bool   `gorm:"column:enable_auth;not null;default:false"`
	EnablePayload bool   `gorm:"column:enable_payload;not null;default:false"`
	Enabled       bool   `gorm:"column:enabled;not null;default:true"`
	CreatedAt     string `gorm:"column:created_at;not null"`
	UpdatedAt     string `gorm:"column:updated_at;not null"`
}

func (Route) TableName() string { return "routes" }

// RouteUpstream connects a route to an upstream model.
type RouteUpstream struct {
	ID         string `gorm:"column:id;primaryKey"`
	RouteID    string `gorm:"column:route_id;not null;uniqueIndex:idx_route_upstream_model;size:191"`
	UpstreamID string `gorm:"column:upstream_id;not null;uniqueIndex:idx_route_upstream_model;size:191"`
	Model      string `gorm:"column:model;not null;uniqueIndex:idx_route_upstream_model;size:255"`
	Weight     int32  `gorm:"column:weight;not null;default:100"`
	Priority   int32  `gorm:"column:priority;not null;default:1"`
	Enabled    bool   `gorm:"column:enabled;not null;default:true"`
	CreatedAt  string `gorm:"column:created_at;not null"`
	UpdatedAt  string `gorm:"column:updated_at;not null"`
}

func (RouteUpstream) TableName() string { return "route_upstreams" }

// Consumer is a downstream caller identity.
type Consumer struct {
	ID                  string `gorm:"column:id;primaryKey"`
	Name                string `gorm:"column:name;uniqueIndex;not null;size:255"`
	Enabled             bool   `gorm:"column:enabled;not null;default:true"`
	MetadataJSON        string `gorm:"column:metadata_json"`
	ProtocolsJSON       string `gorm:"column:protocols_json"`
	IPAllowlistJSON     string `gorm:"column:ip_allowlist_json"`
	MaxInputTokens      int64  `gorm:"column:max_input_tokens"`
	MaxOutputTokens     int64  `gorm:"column:max_output_tokens"`
	MaxRequestBodyBytes int64  `gorm:"column:max_request_body_bytes"`
	CreatedAt           string `gorm:"column:created_at;not null"`
	UpdatedAt           string `gorm:"column:updated_at;not null"`
}

func (Consumer) TableName() string { return "consumers" }

// ConsumerKey is one API key owned by a consumer.
type ConsumerKey struct {
	ID         string `gorm:"column:id;primaryKey"`
	ConsumerID string `gorm:"column:consumer_id;not null;uniqueIndex:idx_consumer_key_name;size:191"`
	Name       string `gorm:"column:name;not null;uniqueIndex:idx_consumer_key_name;size:255"`
	KeyPreview string `gorm:"column:key_preview;not null;index;size:191"`
	KeyHash    string `gorm:"column:key_hash;not null"`
	Enabled    bool   `gorm:"column:enabled;not null;default:true"`
	ExpiresAt  string `gorm:"column:expires_at"`
	LastUsedAt string `gorm:"column:last_used_at"`
	CreatedAt  string `gorm:"column:created_at;not null"`
	UpdatedAt  string `gorm:"column:updated_at;not null"`
}

func (ConsumerKey) TableName() string { return "consumer_keys" }

// ConsumerRoute grants a consumer access to a route.
type ConsumerRoute struct {
	ConsumerID string `gorm:"column:consumer_id;primaryKey"`
	RouteID    string `gorm:"column:route_id;primaryKey"`
}

func (ConsumerRoute) TableName() string { return "consumer_routes" }

// ConsumerQuota is one quota rule for a consumer.
type ConsumerQuota struct {
	ID         string `gorm:"column:id;primaryKey"`
	ConsumerID string `gorm:"column:consumer_id;not null"`
	QuotaType  string `gorm:"column:quota_type;not null"`
	QuotaLimit int64  `gorm:"column:quota_limit;not null"`
	Window     string `gorm:"column:window"`
	Currency   string `gorm:"column:currency"`
	CreatedAt  string `gorm:"column:created_at;not null"`
	UpdatedAt  string `gorm:"column:updated_at;not null"`
}

func (ConsumerQuota) TableName() string { return "consumer_quotas" }

// Setting is a global key-value setting row.
type Setting struct {
	Key       string `gorm:"column:key;primaryKey"`
	Value     string `gorm:"column:value;not null"`
	UpdatedAt string `gorm:"column:updated_at;not null"`
}

func (Setting) TableName() string { return "settings" }

// All returns all canonical SQL models for migration and code generation.
func All() []any {
	return []any{
		&Upstream{},
		&Route{},
		&RouteUpstream{},
		&Consumer{},
		&ConsumerKey{},
		&ConsumerRoute{},
		&ConsumerQuota{},
		&Setting{},
	}
}
