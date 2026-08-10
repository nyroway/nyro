package observability

type StatsOverview struct {
	TotalRequests     int64    `json:"total_requests"`
	TotalInputTokens  int64    `json:"total_input_tokens"`
	TotalOutputTokens int64    `json:"total_output_tokens"`
	AvgDurationMs     float64  `json:"avg_duration_ms"`
	P95DurationMs     *float64 `json:"p95_duration_ms"`
	ErrorCount        int64    `json:"error_count"`
}

type RouteStats struct {
	RouteID           string   `json:"route_id"`
	RouteModel        string   `json:"route_model"`
	RequestCount      int64    `json:"request_count"`
	TotalInputTokens  int64    `json:"total_input_tokens"`
	TotalOutputTokens int64    `json:"total_output_tokens"`
	AvgDurationMs     float64  `json:"avg_duration_ms"`
	P95DurationMs     *float64 `json:"p95_duration_ms"`
	ErrorCount        int64    `json:"error_count"`
}

type UpstreamStats struct {
	UpstreamID    string   `json:"upstream_id"`
	UpstreamName  string   `json:"upstream_name"`
	RequestCount  int64    `json:"request_count"`
	ErrorCount    int64    `json:"error_count"`
	AvgDurationMs float64  `json:"avg_duration_ms"`
	P95DurationMs *float64 `json:"p95_duration_ms"`
}

type ConsumerStats struct {
	ConsumerID        string `json:"consumer_id"`
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
