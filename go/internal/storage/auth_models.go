package storage

// UsageWindow selects a quota window for the auth-access counters.
type UsageWindow int

const (
	WindowMinute UsageWindow = iota
	WindowDay
)

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

// RequestLog is one audit row. The full 31-column Rust schema (column names
// match db/mod.rs INIT_SQL so a Go gateway can read a Rust DB).
type RequestLog struct {
	ID                      string `json:"id" gorm:"column:id;primaryKey"`
	CreatedAt               int64  `json:"created_at" gorm:"column:created_at;index"`
	APIKeyID                string `json:"api_key_id,omitempty" gorm:"column:api_key_id;index"`
	APIKeyName              string `json:"api_key_name,omitempty" gorm:"column:api_key_name"`
	ClientProtocol          string `json:"client_protocol,omitempty" gorm:"column:client_protocol"`
	UpstreamProtocol        string `json:"upstream_protocol,omitempty" gorm:"column:upstream_protocol"`
	ProviderID              string `json:"provider_id,omitempty" gorm:"column:provider_id"`
	ProviderName            string `json:"provider_name,omitempty" gorm:"column:provider_name"`
	ModelID                 string `json:"model_id,omitempty" gorm:"column:model_id"`
	ModelName               string `json:"model_name,omitempty" gorm:"column:model_name"`
	UpstreamURL             string `json:"upstream_url,omitempty" gorm:"column:upstream_url"`
	ClientModel             string `json:"client_model,omitempty" gorm:"column:client_model"`
	UpstreamModel           string `json:"upstream_model,omitempty" gorm:"column:upstream_model"`
	Method                  string `json:"method,omitempty" gorm:"column:method"`
	Path                    string `json:"path,omitempty" gorm:"column:path"`
	ClientRequestHeaders    string `json:"client_request_headers,omitempty" gorm:"column:client_request_headers"`
	ClientRequestBody       string `json:"client_request_body,omitempty" gorm:"column:client_request_body"`
	ClientResponseHeaders   string `json:"client_response_headers,omitempty" gorm:"column:client_response_headers"`
	ClientResponseBody      string `json:"client_response_body,omitempty" gorm:"column:client_response_body"`
	UpstreamRequestHeaders  string `json:"upstream_request_headers,omitempty" gorm:"column:upstream_request_headers"`
	UpstreamRequestBody     string `json:"upstream_request_body,omitempty" gorm:"column:upstream_request_body"`
	UpstreamResponseHeaders string `json:"upstream_response_headers,omitempty" gorm:"column:upstream_response_headers"`
	UpstreamResponseBody    string `json:"upstream_response_body,omitempty" gorm:"column:upstream_response_body"`
	UpstreamStatusCode      *int32 `json:"upstream_status_code,omitempty" gorm:"column:upstream_status_code"`
	ClientStatusCode        *int32 `json:"client_status_code,omitempty" gorm:"column:client_status_code"`
	LatencyTotalMs          *int64 `json:"latency_total_ms,omitempty" gorm:"column:latency_total_ms"`
	LatencyUpstreamMs       *int64 `json:"latency_upstream_ms,omitempty" gorm:"column:latency_upstream_ms"`
	InputTokens             int32  `json:"input_tokens" gorm:"column:input_tokens;default:0"`
	OutputTokens            int32  `json:"output_tokens" gorm:"column:output_tokens;default:0"`
	CacheReadTokens         int32  `json:"cache_read_tokens" gorm:"column:cache_read_tokens;default:0"`
	IsStream                bool   `json:"is_stream" gorm:"column:is_stream"`
	StreamChunksCount       int32  `json:"stream_chunks_count,omitempty" gorm:"column:stream_chunks_count"`
	StreamFirstChunkMs      *int64 `json:"stream_first_chunk_ms,omitempty" gorm:"column:stream_first_chunk_ms"`
}

// TableName fixes the request_logs table name.
func (RequestLog) TableName() string { return "request_logs" }

// LogQuery filters a log page query.
type LogQuery struct {
	Limit     int64  `json:"limit,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	StatusMin *int32 `json:"status_min,omitempty"`
	StatusMax *int32 `json:"status_max,omitempty"`
}

// LogPage is a page of logs.
type LogPage struct {
	Items []RequestLog `json:"items"`
	Total int64        `json:"total"`
}

// StatsOverview is the dashboard summary.
type StatsOverview struct {
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
	ErrorCount        int64   `json:"error_count"`
}

// ModelStats aggregates request/token/latency per model.
type ModelStats struct {
	Model             string  `json:"model"`
	RequestCount      int64   `json:"request_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}

// ProviderStats aggregates request/error/latency per provider.
type ProviderStats struct {
	Provider      string  `json:"provider"`
	RequestCount  int64   `json:"request_count"`
	ErrorCount    int64   `json:"error_count"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

// ApiKeyStats aggregates request/token/cache stats per API key.
type ApiKeyStats struct {
	APIKeyID          string `json:"api_key_id"`
	APIKeyName        string `json:"api_key_name"`
	RequestCount      int64  `json:"request_count"`
	TotalInputTokens  int64  `json:"total_input_tokens"`
	TotalOutputTokens int64  `json:"total_output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	LastUsedAt        int64  `json:"last_used_at"`
}

// StatsHourly aggregates request/token/error per hour bucket.
type StatsHourly struct {
	Hour              string  `json:"hour"`
	RequestCount      int64   `json:"request_count"`
	ErrorCount        int64   `json:"error_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}

// ProviderTestResult records a connectivity test outcome.
type ProviderTestResult struct {
	Success  bool   `json:"success"`
	TestedAt string `json:"tested_at"`
}
