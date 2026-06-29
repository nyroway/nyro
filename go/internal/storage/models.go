// Package storage defines the persistence layer for the Nyro gateway: the
// data models (port of crates/nyro-core/src/db/models.rs), the Storage
// interface and its typed sub-stores (port of storage/traits.rs), and the
// backends (sqlite/postgres/mysql/memory).
//
// JSON tags match the Rust serde wire format (snake_case) so the admin API and
// import/export are compatible with the existing React WebUI. GORM tags map the
// same models to SQL columns (AutoMigrate is OFF; the schema is applied by each
// backend's bootstrap via explicit CREATE TABLE IF NOT EXISTS).
package storage

// ModelBalance selects the target-selection strategy for a model's backends.
type ModelBalance string

const (
	BalanceWeighted ModelBalance = "weighted"
	BalancePriority ModelBalance = "priority"
	BalanceCooldown ModelBalance = "cooldown"
	BalanceLatency  ModelBalance = "latency"
)

// ParseModelBalance resolves a balance string (empty → weighted).
func ParseModelBalance(s string) (ModelBalance, bool) {
	switch s {
	case "", "weighted":
		return BalanceWeighted, true
	case "priority":
		return BalancePriority, true
	case "cooldown":
		return BalanceCooldown, true
	case "latency":
		return BalanceLatency, true
	}
	return "", false
}

// Provider is an upstream provider configuration row.
type Provider struct {
	ID              string `json:"id" gorm:"column:id;primaryKey"`
	Name            string `json:"name" gorm:"column:name"`
	Vendor          string `json:"vendor,omitempty" gorm:"column:vendor"`
	Protocol        string `json:"protocol" gorm:"column:protocol"`
	BaseURL         string `json:"base_url" gorm:"column:base_url"`
	PresetKey       string `json:"preset_key,omitempty" gorm:"column:preset_key"`
	Channel         string `json:"channel,omitempty" gorm:"column:channel"`
	ModelsSource    string `json:"models_source,omitempty" gorm:"column:models_source"`
	StaticModels    string `json:"static_models,omitempty" gorm:"column:static_models"`
	APIKey          string `json:"api_key" gorm:"column:api_key"`
	AuthMode        string `json:"auth_mode" gorm:"column:auth_mode;default:apikey"`
	UseProxy        bool   `json:"use_proxy" gorm:"column:use_proxy"`
	LastTestSuccess *bool  `json:"last_test_success,omitempty" gorm:"column:last_test_success"`
	LastTestAt      string `json:"last_test_at,omitempty" gorm:"column:last_test_at"`
	IsEnabled       bool   `json:"is_enabled" gorm:"column:is_enabled;default:true"`
	CreatedAt       string `json:"created_at" gorm:"column:created_at"`
	UpdatedAt       string `json:"updated_at" gorm:"column:updated_at"`
}

// TableName fixes the providers table name.
func (Provider) TableName() string { return "providers" }

// EffectiveAuthMode returns the auth mode, defaulting to "apikey".
func (p Provider) EffectiveAuthMode() string {
	if p.AuthMode == "" {
		return "apikey"
	}
	return p.AuthMode
}

// Model is a client-facing route mapping a virtual model name to backends.
type Model struct {
	ID             string         `json:"id" gorm:"column:id;primaryKey"`
	Name           string         `json:"name" gorm:"column:name"`
	Balance        ModelBalance   `json:"balance" gorm:"column:balance;default:weighted"`
	TargetProvider string         `json:"target_provider,omitempty" gorm:"column:target_provider"`
	TargetModel    string         `json:"target_model,omitempty" gorm:"column:target_model"`
	EnableAuth     bool           `json:"enable_auth" gorm:"column:enable_auth"`
	EnablePayload  *bool          `json:"enable_payload,omitempty" gorm:"column:enable_payload"`
	IsEnabled      bool           `json:"is_enabled" gorm:"column:is_enabled;default:true"`
	CreatedAt      string         `json:"created_at" gorm:"column:created_at"`
	Targets        []ModelBackend `json:"targets" gorm:"-"`
}

// TableName fixes the models table name.
func (Model) TableName() string { return "models" }

// ModelBackend is one upstream target for a model.
type ModelBackend struct {
	ID         string `json:"id" gorm:"column:id;primaryKey"`
	ModelID    string `json:"model_id" gorm:"column:model_id"`
	ProviderID string `json:"provider_id" gorm:"column:provider_id"`
	Model      string `json:"model" gorm:"column:model"`
	Weight     int32  `json:"weight" gorm:"column:weight"`
	Priority   int32  `json:"priority" gorm:"column:priority"`
	CreatedAt  string `json:"created_at" gorm:"column:created_at"`
}

// TableName fixes the model_backends table name.
func (ModelBackend) TableName() string { return "model_backends" }

// Setting is a key-value config row. The DB column is `name` (matching the
// Rust schema); the admin API exposes it as the JSON/path "key".
type Setting struct {
	Key       string `json:"key" gorm:"column:name;primaryKey"`
	Value     string `json:"value" gorm:"column:value"`
	UpdatedAt string `json:"updated_at,omitempty" gorm:"column:updated_at"`
}

// TableName fixes the settings table name.
func (Setting) TableName() string { return "settings" }

// ApiKeyModel is one (api_key, model) binding row.
type ApiKeyModel struct {
	APIKeyID string `json:"api_key_id" gorm:"column:api_key_id;primaryKey"`
	ModelID  string `json:"model_id" gorm:"column:model_id;primaryKey"`
}

// TableName fixes the api_key_models table name.
func (ApiKeyModel) TableName() string { return "api_key_models" }

// ── Write DTOs ───────────────────────────────────────────────────────────────

// CreateProvider holds the fields for creating a provider.
type CreateProvider struct {
	Name         string `json:"name"`
	Vendor       string `json:"vendor,omitempty"`
	Protocol     string `json:"protocol"`
	BaseURL      string `json:"base_url"`
	PresetKey    string `json:"preset_key,omitempty"`
	Channel      string `json:"channel,omitempty"`
	ModelsSource string `json:"models_source,omitempty"`
	StaticModels string `json:"static_models,omitempty"`
	APIKey       string `json:"api_key"`
	AuthMode     string `json:"auth_mode,omitempty"`
	UseProxy     bool   `json:"use_proxy,omitempty"`
}

// UpdateProvider holds optional fields for a partial update (nil = unchanged).
type UpdateProvider struct {
	Name         *string `json:"name,omitempty"`
	Vendor       *string `json:"vendor,omitempty"`
	Protocol     *string `json:"protocol,omitempty"`
	BaseURL      *string `json:"base_url,omitempty"`
	PresetKey    *string `json:"preset_key,omitempty"`
	Channel      *string `json:"channel,omitempty"`
	ModelsSource *string `json:"models_source,omitempty"`
	StaticModels *string `json:"static_models,omitempty"`
	APIKey       *string `json:"api_key,omitempty"`
	AuthMode     *string `json:"auth_mode,omitempty"`
	UseProxy     *bool   `json:"use_proxy,omitempty"`
	IsEnabled    *bool   `json:"is_enabled,omitempty"`
}

// CreateModelBackend is one target in a create/update model payload.
type CreateModelBackend struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Weight     int32  `json:"weight,omitempty"`
	Priority   int32  `json:"priority,omitempty"`
}

// CreateModel holds the fields for creating a model route.
type CreateModel struct {
	Name          string               `json:"name"`
	Balance       ModelBalance         `json:"balance,omitempty"`
	EnableAuth    bool                 `json:"enable_auth,omitempty"`
	EnablePayload *bool                `json:"enable_payload,omitempty"`
	Targets       []CreateModelBackend `json:"targets"`
}

// UpdateModel holds optional fields for a partial model update.
type UpdateModel struct {
	Name          *string               `json:"name,omitempty"`
	Balance       *ModelBalance         `json:"balance,omitempty"`
	EnableAuth    *bool                 `json:"enable_auth,omitempty"`
	EnablePayload *bool                 `json:"enable_payload,omitempty"`
	IsEnabled     *bool                 `json:"is_enabled,omitempty"`
	Targets       *[]CreateModelBackend `json:"targets,omitempty"`
}
