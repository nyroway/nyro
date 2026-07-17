package observability

import (
	"crypto/rand"
	"encoding/hex"
)

// LogCtx carries the per-request fields captured across the dispatch lifecycle
// (protocols, models, method/path, upstream status/latency) so the OnLog hook
// can populate the audit LogRecord + metric/span attributes. Mirrors the Rust
// LogBuilder shape; fields are EXPORTED so the dispatcher (package proxy) can
// populate them when wiring T3.3.
//
// This is the observability copy. The proxy package retains its own unexported
// logCtx for now (T3.3 switches the dispatcher to this one and deletes the
// proxy original, keeping T3.2 and T3.3 independently green).
type LogCtx struct {
	APIKeyName        string
	APIKeyPreview     string
	ClientProtocol    string
	UpstreamProtocol  string
	ClientModel       string
	UpstreamModel     string
	Method            string
	Path              string
	IsStream          bool
	UpstreamStatus    *int32
	LatencyUpstreamMs *int64
}

// NewRequestID returns a short random request identifier ("req_" + 16 hex
// chars). It is the observability copy of proxy.newRequestID; non-fatal on
// rand failure (id remains valid, just less random). Used by the OnLog hook to
// stamp nyro.log.id on the audit LogRecord.
func NewRequestID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "req_" + hex.EncodeToString(buf[:])
}

// LogRecord is one request-audit row. JSON tags are identical to the legacy
// storage.RequestLog so the WebUI contract is unchanged. parquet tags define
// the columnar schema; zstd compression is applied per-column via struct tags.
type LogRecord struct {
	ID                 string `json:"id" parquet:"id,snappy"`
	CreatedAt          int64  `json:"created_at" parquet:"created_at"`
	APIKeyID           string `json:"api_key_id,omitempty" parquet:"api_key_id,dict"`
	APIKeyName         string `json:"api_key_name,omitempty" parquet:"api_key_name"`
	APIKeyPreview      string `json:"api_key_preview,omitempty" parquet:"api_key_preview"`
	ClientProtocol     string `json:"client_protocol,omitempty" parquet:"client_protocol,dict"`
	UpstreamProtocol   string `json:"upstream_protocol,omitempty" parquet:"upstream_protocol,dict"`
	ProviderID         string `json:"provider_id,omitempty" parquet:"provider_id,dict"`
	ProviderName       string `json:"provider_name,omitempty" parquet:"provider_name"`
	ModelID            string `json:"model_id,omitempty" parquet:"model_id,dict"`
	ModelName          string `json:"model_name,omitempty" parquet:"model_name"`
	ClientModel        string `json:"client_model,omitempty" parquet:"client_model"`
	UpstreamModel      string `json:"upstream_model,omitempty" parquet:"upstream_model"`
	Method             string `json:"method,omitempty" parquet:"method,dict"`
	Path               string `json:"path,omitempty" parquet:"path"`
	ClientStatusCode   *int32 `json:"client_status_code,omitempty" parquet:"client_status_code,optional"`
	UpstreamStatusCode *int32 `json:"upstream_status_code,omitempty" parquet:"upstream_status_code,optional"`
	LatencyTotalMs     *int64 `json:"latency_total_ms,omitempty" parquet:"latency_total_ms,optional"`
	LatencyUpstreamMs  *int64 `json:"latency_upstream_ms,omitempty" parquet:"latency_upstream_ms,optional"`
	InputTokens        int32  `json:"input_tokens" parquet:"input_tokens"`
	OutputTokens       int32  `json:"output_tokens" parquet:"output_tokens"`
	CacheReadTokens    int32  `json:"cache_read_tokens" parquet:"cache_read_tokens"`
	IsStream           bool   `json:"is_stream" parquet:"is_stream"`
}

// MetricSample is one point of a metrics time-series snapshot (one OTLP
// metric export = one snapshot = many samples).
type MetricSample struct {
	Ts         int64   `parquet:"ts"`
	Name       string  `parquet:"name,dict"`
	LabelsJSON string  `parquet:"labels_json"`
	Kind       string  `parquet:"kind,dict"` // "counter" | "histogram" | "gauge"
	Value      float64 `parquet:"value"`
	HistSum    float64 `parquet:"hist_sum"`
	HistCount  int64   `parquet:"hist_count"`
}

// SpanSnapshot is one trace span, as written to parquet by the admin receiver.
type SpanSnapshot struct {
	TraceID      string `parquet:"trace_id,dict"`
	SpanID       string `parquet:"span_id"`
	ParentSpanID string `parquet:"parent_span_id"`
	Name         string `parquet:"name,dict"`
	StartNs      int64  `parquet:"start_ns"`
	EndNs        int64  `parquet:"end_ns"`
	DurationNs   int64  `parquet:"duration_ns"`
	StatusCode   int32  `parquet:"status_code"`
	AttrsJSON    string `parquet:"attrs_json"`
}

// MetricLabels is the fixed label set for nyro metrics (bounded cardinality).
type MetricLabels struct {
	Model       string
	Provider    string
	APIKey      string
	StatusClass string // "2xx" | "4xx" | "5xx"
}
