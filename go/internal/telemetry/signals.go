package telemetry

import (
	"crypto/rand"
	"encoding/hex"
)

// LogCtx carries the per-request fields captured across the dispatch lifecycle
// (protocols, models, method/path, upstream status/latency) so the telemetry
// Stage can populate the audit LogRecord + metric/span attributes. Mirrors the Rust
// LogBuilder shape; fields are EXPORTED so the dispatcher (package proxy) can
// populate them when wiring T3.3.
//
// This is the observability copy. The proxy package retains its own unexported
// logCtx for now (T3.3 switches the dispatcher to this one and deletes the
// proxy original, keeping T3.2 and T3.3 independently green).
type LogCtx struct {
	ConsumerKeyName    string
	ConsumerKeyPreview string
	ClientProtocol     string
	UpstreamProtocol   string
	ClientModel        string
	UpstreamModel      string
	Method             string
	Path               string
	IsStream           bool
	UpstreamStatus     *int32
	LatencyUpstreamMs  *int64
}

// NewRequestID returns a short random request identifier ("req_" + 16 hex
// chars). It is the observability copy of proxy.newRequestID; non-fatal on
// rand failure (id remains valid, just less random). Used by the telemetry
// Stage to stamp nyro.log.id on the audit LogRecord.
func NewRequestID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "req_" + hex.EncodeToString(buf[:])
}

// LogRecord is the admin-facing request audit projection decoded from OTLP.
type LogRecord struct {
	ID                 string `json:"id"`
	CreatedAt          int64  `json:"created_at"`
	ConsumerID         string `json:"consumer_id,omitempty"`
	ConsumerKeyName    string `json:"consumer_key_name,omitempty"`
	ConsumerKeyPreview string `json:"consumer_key_preview,omitempty"`
	ClientProtocol     string `json:"client_protocol,omitempty"`
	UpstreamProtocol   string `json:"upstream_protocol,omitempty"`
	UpstreamID         string `json:"upstream_id,omitempty"`
	UpstreamName       string `json:"upstream_name,omitempty"`
	RouteID            string `json:"route_id,omitempty"`
	RouteModel         string `json:"route_model,omitempty"`
	ClientModel        string `json:"client_model,omitempty"`
	UpstreamModel      string `json:"upstream_model,omitempty"`
	Method             string `json:"method,omitempty"`
	Path               string `json:"path,omitempty"`
	ResponseStatusCode *int32 `json:"response_status_code,omitempty"`
	UpstreamStatusCode *int32 `json:"upstream_status_code,omitempty"`
	LatencyTotalMs     *int64 `json:"latency_total_ms,omitempty"`
	LatencyUpstreamMs  *int64 `json:"latency_upstream_ms,omitempty"`
	InputTokens        int32  `json:"input_tokens"`
	OutputTokens       int32  `json:"output_tokens"`
	CacheReadTokens    int32  `json:"cache_read_tokens"`
	IsStream           bool   `json:"is_stream"`
}

// MetricSample is one point of a metrics time-series snapshot (one OTLP
// metric export = one snapshot = many samples).
type MetricSample struct {
	Ts          int64
	Name        string
	LabelsJSON  string
	Kind        string // "counter" | "histogram" | "gauge"
	Value       float64
	HistSum     float64
	HistCount   int64
	HistBounds  []float64
	HistBuckets []uint64
}
