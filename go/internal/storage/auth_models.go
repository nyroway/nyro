package storage

// ApiKey is a gateway access token with optional rate/token quotas.
type ApiKey struct {
	ID        string `json:"id" gorm:"column:id;primaryKey"`
	Token     string `json:"key" gorm:"column:token"`
	Name      string `json:"name" gorm:"column:name"`
	RPM       *int32 `json:"rpm,omitempty" gorm:"column:rpm"`
	RPD       *int32 `json:"rpd,omitempty" gorm:"column:rpd"`
	TPM       *int32 `json:"tpm,omitempty" gorm:"column:tpm"`
	TPD       *int32 `json:"tpd,omitempty" gorm:"column:tpd"`
	IsEnabled bool   `json:"is_enabled" gorm:"column:is_enabled;default:true"`
	ExpiresAt string `json:"expires_at,omitempty" gorm:"column:expires_at"`
	CreatedAt string `json:"created_at" gorm:"column:created_at"`
	UpdatedAt string `json:"updated_at" gorm:"column:updated_at"`
}

// TableName fixes the api_keys table name.
func (ApiKey) TableName() string { return "api_keys" }

// ApiKeyWithBindings is an ApiKey plus the model ids it is bound to.
type ApiKeyWithBindings struct {
	ApiKey
	ModelIDs []string `json:"model_ids" gorm:"-"`
}

// ApiKeyAccessRecord is the read model used by the inbound auth check.
type ApiKeyAccessRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsEnabled bool   `json:"is_enabled"`
	ExpiresAt string `json:"expires_at,omitempty"`
	RPM       *int32 `json:"rpm,omitempty"`
	RPD       *int32 `json:"rpd,omitempty"`
	TPM       *int32 `json:"tpm,omitempty"`
	TPD       *int32 `json:"tpd,omitempty"`
}

// CreateApiKey holds the fields for creating an API key.
type CreateApiKey struct {
	Name      string   `json:"name"`
	Token     string   `json:"token,omitempty"` // optional explicit token; empty → auto-generated
	RPM       *int32   `json:"rpm,omitempty"`
	RPD       *int32   `json:"rpd,omitempty"`
	TPM       *int32   `json:"tpm,omitempty"`
	TPD       *int32   `json:"tpd,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	ModelIDs  []string `json:"model_ids,omitempty"`
}

// UpdateApiKey holds optional fields for a partial API-key update.
type UpdateApiKey struct {
	Name      *string   `json:"name,omitempty"`
	RPM       *int32    `json:"rpm,omitempty"`
	RPD       *int32    `json:"rpd,omitempty"`
	TPM       *int32    `json:"tpm,omitempty"`
	TPD       *int32    `json:"tpd,omitempty"`
	IsEnabled *bool     `json:"is_enabled,omitempty"`
	ExpiresAt *string   `json:"expires_at,omitempty"`
	ModelIDs  *[]string `json:"model_ids,omitempty"`
}

// OAuthCredential is a stored upstream OAuth token (CAS-refreshed).
type OAuthCredential struct {
	ProviderID    string `json:"provider_id" gorm:"column:provider_id;primaryKey"`
	DriverKey     string `json:"driver_key" gorm:"column:driver_key"`
	Scheme        string `json:"scheme" gorm:"column:scheme"`
	AccessToken   string `json:"access_token" gorm:"column:access_token"`
	RefreshToken  string `json:"refresh_token,omitempty" gorm:"column:refresh_token"`
	ExpiresAt     string `json:"expires_at,omitempty" gorm:"column:expires_at"`
	ResourceURL   string `json:"resource_url,omitempty" gorm:"column:resource_url"`
	SubjectID     string `json:"subject_id,omitempty" gorm:"column:subject_id"`
	Scopes        string `json:"scopes,omitempty" gorm:"column:scopes"`
	Meta          string `json:"meta,omitempty" gorm:"column:meta"`
	Status        string `json:"status" gorm:"column:status;default:connected"`
	StatusVersion int32  `json:"status_version" gorm:"column:status_version;default:0"`
	LastError     string `json:"last_error,omitempty" gorm:"column:last_error"`
	LastRefreshAt string `json:"last_refresh_at,omitempty" gorm:"column:last_refresh_at"`
	CreatedAt     string `json:"created_at" gorm:"column:created_at"`
	UpdatedAt     string `json:"updated_at" gorm:"column:updated_at"`
}

// TableName fixes the provider_oauth_credentials table name.
func (OAuthCredential) TableName() string { return "provider_oauth_credentials" }

// UpsertOAuthCredential holds the fields written on token store/refresh.
type UpsertOAuthCredential struct {
	DriverKey    string `json:"driver_key"`
	Scheme       string `json:"scheme"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ResourceURL  string `json:"resource_url,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
	Meta         string `json:"meta,omitempty"`
}

// ProviderTestResult records a connectivity test outcome.
type ProviderTestResult struct {
	Success  bool   `json:"success"`
	TestedAt string `json:"tested_at"`
}
