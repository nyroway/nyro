package observability

// Stats* types are JSON-compatible copies of storage.Stats* (Phase 4 removes
// the storage copies). Tag-for-tag identical so the WebUI is unaffected.

type StatsOverview struct {
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
	ErrorCount        int64   `json:"error_count"`
}

type ModelStats struct {
	Model             string  `json:"model"`
	RequestCount      int64   `json:"request_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}

type ProviderStats struct {
	Provider      string  `json:"provider"`
	RequestCount  int64   `json:"request_count"`
	ErrorCount    int64   `json:"error_count"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

type ApiKeyStats struct {
	APIKeyID          string `json:"api_key_id"`
	APIKeyName        string `json:"api_key_name"`
	RequestCount      int64  `json:"request_count"`
	TotalInputTokens  int64  `json:"total_input_tokens"`
	TotalOutputTokens int64  `json:"total_output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	LastUsedAt        int64  `json:"last_used_at"`
}

type StatsHourly struct {
	Hour              string  `json:"hour"`
	RequestCount      int64   `json:"request_count"`
	ErrorCount        int64   `json:"error_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}
