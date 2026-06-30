package observability

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// Receiver is the admin-side OTLP/HTTP receiver. It decodes the official OTLP
// protobuf and hands rows to the parquet sinks. Receive-then-ACK: decode is
// synchronous, parquet write is buffered (flushed by the sink on rotation and
// on Flush). The Receiver itself is stateless beyond the sink pointers; each
// parquet Sink carries its own mutex, so the handlers are safe to call
// concurrently across the three routes.
type Receiver struct {
	logs    *parquet.Sink[LogRecord]
	metrics *parquet.Sink[MetricSample]
	traces  *parquet.Sink[SpanSnapshot]
}

// NewReceiver wires the three parquet sinks to a fresh Receiver.
func NewReceiver(logs *parquet.Sink[LogRecord], metrics *parquet.Sink[MetricSample], traces *parquet.Sink[SpanSnapshot]) *Receiver {
	return &Receiver{logs: logs, metrics: metrics, traces: traces}
}

// Mount registers POST /v1/{logs,metrics,traces} on the given chi router.
// Routes are top-level (NOT under /api/v1) and are not behind admin-token auth;
// the auth boundary is applied by the admin wiring (T2.2).
func (rcv *Receiver) Mount(r chi.Router) {
	r.Post("/v1/logs", rcv.handleLogs)
	r.Post("/v1/metrics", rcv.handleMetrics)
	r.Post("/v1/traces", rcv.handleTraces)
}

// Flush forces every buffered row into its parquet file.
func (rcv *Receiver) Flush(ctx context.Context) {
	_ = rcv.logs.Flush()
	_ = rcv.metrics.Flush()
	_ = rcv.traces.Flush()
}

func (rcv *Receiver) handleLogs(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collectlogs.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []LogRecord
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				rows = append(rows, logRecordFromOTLP(lr))
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.logs.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collectlogs.ExportLogsServiceResponse{})
	_, _ = w.Write(wb)
}

// logRecordFromOTLP maps the gateway's OTLP attribute contract onto a LogRecord.
// String keys default to ""; numeric keys default to 0 when absent. Optional
// pointer fields (status codes, latencies) are left nil unless their key is
// present (and non-zero, so a 0-valued optional is treated as absent — acceptable
// for nyro's request-audit contract, where latencies/status codes are non-zero).
func logRecordFromOTLP(lr *logsv1.LogRecord) LogRecord {
	m := attrMap(lr.GetAttributes())
	rec := LogRecord{
		ID:               m.str("nyro.log.id"),
		ClientProtocol:   m.str("nyro.client_protocol"),
		UpstreamProtocol: m.str("nyro.upstream_protocol"),
		ProviderID:       m.str("nyro.provider_id"), ProviderName: m.str("nyro.provider_name"),
		ModelID: m.str("nyro.model_id"), ModelName: m.str("nyro.model_name"),
		ClientModel: m.str("nyro.client_model"), UpstreamModel: m.str("nyro.upstream_model"),
		Method: m.str("nyro.method"), Path: m.str("nyro.path"),
		APIKeyID: m.str("nyro.api_key_id"), APIKeyName: m.str("nyro.api_key_name"),
		InputTokens: int32(m.i("nyro.input_tokens")), OutputTokens: int32(m.i("nyro.output_tokens")),
		CacheReadTokens: int32(m.i("nyro.cache_read_tokens")),
	}
	if v, ok := m.lookupInt("nyro.log.created_ms"); ok {
		rec.CreatedAt = v
	}
	if v, ok := m.lookupInt("nyro.client_status"); ok {
		c := int32(v)
		rec.ClientStatusCode = &c
	}
	if v, ok := m.lookupInt("nyro.upstream_status"); ok {
		c := int32(v)
		rec.UpstreamStatusCode = &c
	}
	if v, ok := m.lookupInt("nyro.latency_total_ms"); ok {
		rec.LatencyTotalMs = &v
	}
	if v, ok := m.lookupInt("nyro.latency_upstream_ms"); ok {
		rec.LatencyUpstreamMs = &v
	}
	rec.IsStream = m.bool("nyro.is_stream")
	return rec
}

func (rcv *Receiver) handleMetrics(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collectmetrics.ExportMetricsServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []MetricSample
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				name := m.GetName()
				switch d := m.Data.(type) {
				case *metricsv1.Metric_Gauge:
					for _, p := range d.Gauge.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "gauge",
							Value: dataPointValue(p), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				case *metricsv1.Metric_Sum:
					for _, p := range d.Sum.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "counter",
							Value: dataPointValue(p), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				case *metricsv1.Metric_Histogram:
					for _, p := range d.Histogram.GetDataPoints() {
						rows = append(rows, MetricSample{Ts: int64(p.GetTimeUnixNano()), Name: name, Kind: "histogram",
							HistSum: p.GetSum(), HistCount: int64(p.GetCount()), LabelsJSON: attrsToJSON(p.GetAttributes())})
					}
				}
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.metrics.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collectmetrics.ExportMetricsServiceResponse{})
	_, _ = w.Write(wb)
}

func (rcv *Receiver) handleTraces(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collecttrace.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(b, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rows []SpanSnapshot
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				rows = append(rows, SpanSnapshot{
					TraceID:      hex.EncodeToString(sp.GetTraceId()),
					SpanID:       hex.EncodeToString(sp.GetSpanId()),
					ParentSpanID: hex.EncodeToString(sp.GetParentSpanId()),
					Name:         sp.GetName(),
					StartNs:      int64(sp.GetStartTimeUnixNano()),
					EndNs:        int64(sp.GetEndTimeUnixNano()),
					DurationNs:   int64(sp.GetEndTimeUnixNano() - sp.GetStartTimeUnixNano()),
					StatusCode:   int32(sp.GetStatus().GetCode()),
					AttrsJSON:    attrsToJSON(sp.GetAttributes()),
				})
			}
		}
	}
	if len(rows) > 0 {
		if err := rcv.traces.Write(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	wb, _ := proto.Marshal(&collecttrace.ExportTraceServiceResponse{})
	_, _ = w.Write(wb)
}

// dataPointValue extracts the scalar value of a NumberDataPoint (int or double).
func dataPointValue(p *metricsv1.NumberDataPoint) float64 {
	if v, ok := p.Value.(*metricsv1.NumberDataPoint_AsDouble); ok {
		return v.AsDouble
	}
	if v, ok := p.Value.(*metricsv1.NumberDataPoint_AsInt); ok {
		return float64(v.AsInt)
	}
	return 0
}

// attrsToJSON flattens an OTLP attribute set to a compact JSON object string
// (sufficient for nyro's bounded label set).
func attrsToJSON(kvs []*commonv1.KeyValue) string {
	if len(kvs) == 0 {
		return "{}"
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = anyValue(kv.GetValue())
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func anyValue(v *commonv1.AnyValue) any {
	switch x := v.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return x.StringValue
	case *commonv1.AnyValue_IntValue:
		return x.IntValue
	case *commonv1.AnyValue_BoolValue:
		return x.BoolValue
	case *commonv1.AnyValue_DoubleValue:
		return x.DoubleValue
	default:
		return v.String()
	}
}

// --- attribute helpers (the logs path reads known keys by name) ---

type attrTable struct{ m map[string]*commonv1.KeyValue }

func attrMap(attrs []*commonv1.KeyValue) attrTable {
	out := attrTable{m: map[string]*commonv1.KeyValue{}}
	for _, kv := range attrs {
		out.m[kv.GetKey()] = kv
	}
	return out
}

func (a attrTable) str(k string) string {
	if kv, ok := a.m[k]; ok {
		return kv.GetValue().GetStringValue()
	}
	return ""
}

func (a attrTable) i(k string) int64 {
	if v, ok := a.lookupInt(k); ok {
		return v
	}
	return 0
}

func (a attrTable) bool(k string) bool {
	if kv, ok := a.m[k]; ok {
		return kv.GetValue().GetBoolValue()
	}
	return false
}

// lookupInt returns the int value of key k. ok is true only when the key is
// present AND non-zero — a present-but-zero int is indistinguishable from
// absent under the OTLP int-value encoding nyro emits (latencies/status codes
// are always non-zero in practice). Callers needing to distinguish zero from
// absent must use the parquet optional columns directly.
func (a attrTable) lookupInt(k string) (int64, bool) {
	if kv, ok := a.m[k]; ok {
		if v := kv.GetValue().GetIntValue(); v != 0 {
			return v, true
		}
	}
	return 0, false
}
