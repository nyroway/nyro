package observability_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestReceiverLogs(t *testing.T) {
	dir := t.TempDir()
	ls, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	ms, _ := parquet.NewSink[observability.MetricSample](dir, "metrics", 100)
	ts, _ := parquet.NewSink[observability.SpanSnapshot](dir, "traces", 100)
	rcv := observability.NewReceiver(ls, ms, ts)

	r := chi.NewRouter()
	rcv.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	req := &collectlogs.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{{
			ScopeLogs: []*logsv1.ScopeLogs{{
				LogRecords: []*logsv1.LogRecord{{
					Attributes: nyroLogAttrs("req_xyz", "gpt", "openai", "2xx"),
				}},
			}},
		}},
	}
	body, _ := proto.Marshal(req)
	resp, err := http.Post(srv.URL+"/v1/logs", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	rcv.Flush(context.Background())

	got, err := parquet.ReadSince[observability.LogRecord](dir, "logs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "req_xyz" {
		t.Fatalf("unexpected logs: %+v", got)
	}
}

// nyroLogAttrs builds the attribute set the gateway emits for one request log,
// matching the receiver's attribute-key contract (Step 3).
func nyroLogAttrs(id, model, provider, statusClass string) []*commonv1.KeyValue {
	str := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
	}
	return []*commonv1.KeyValue{
		str("nyro.log.id", id),
		str("nyro.model_name", model),
		str("nyro.provider_name", provider),
		str("nyro.status_class", statusClass),
	}
}

func TestReceiverMetrics(t *testing.T) {
	dir := t.TempDir()
	ls, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	ms, _ := parquet.NewSink[observability.MetricSample](dir, "metrics", 100)
	ts, _ := parquet.NewSink[observability.SpanSnapshot](dir, "traces", 100)
	rcv := observability.NewReceiver(ls, ms, ts)

	r := chi.NewRouter()
	rcv.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	intAttr := func(k string, v int64) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: v}}}
	}
	strAttr := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
	}

	req := &collectmetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricsv1.ResourceMetrics{{
			ScopeMetrics: []*metricsv1.ScopeMetrics{{
				Metrics: []*metricsv1.Metric{{
					Name: "nyro.request.total",
					Data: &metricsv1.Metric_Sum{
						Sum: &metricsv1.Sum{
							IsMonotonic: true,
							DataPoints: []*metricsv1.NumberDataPoint{{
								TimeUnixNano: 1_700_000_000_000_000_000,
								Value:        &metricsv1.NumberDataPoint_AsInt{AsInt: 3},
								Attributes:   []*commonv1.KeyValue{strAttr("nyro.provider_name", "openai"), intAttr("nyro.client_status", 200)},
							}},
						},
					},
				}},
			}},
		}},
	}
	body, _ := proto.Marshal(req)
	resp, err := http.Post(srv.URL+"/v1/metrics", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	rcv.Flush(context.Background())

	got, err := parquet.ReadSince[observability.MetricSample](dir, "metrics", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 metric sample, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "nyro.request.total" || got[0].Kind != "counter" || got[0].Value != 3 {
		t.Fatalf("unexpected metric sample: %+v", got[0])
	}
	// Assert the labels CONTENT (not just non-empty) so a label-key swap or
	// dropped attribute would be caught.
	var labels map[string]any
	if err := json.Unmarshal([]byte(got[0].LabelsJSON), &labels); err != nil {
		t.Fatalf("labels json not parseable: %q: %v", got[0].LabelsJSON, err)
	}
	if labels["nyro.provider_name"] != any("openai") {
		t.Fatalf("labels[nyro.provider_name]=%v want openai (full=%q)", labels["nyro.provider_name"], got[0].LabelsJSON)
	}
	// json.Unmarshal decodes numbers into float64, so the int64(200) emitted by
	// anyValue comes back as float64(200). Compare numerically.
	if v, ok := labels["nyro.client_status"].(float64); !ok || v != 200 {
		t.Fatalf("labels[nyro.client_status]=%v want 200 (full=%q)", labels["nyro.client_status"], got[0].LabelsJSON)
	}
}

// TestReceiverTraces round-trips one OTLP span and asserts every field that
// the receiver maps: trace_id/span_id/parent_span_id (hex-encoded), name,
// start/end unix-nanos, the derived duration, status code, and the attribute
// payload. A swap of Start/End, a swap of trace_id/span_id, or a wrong duration
// would fail here.
func TestReceiverTraces(t *testing.T) {
	dir := t.TempDir()
	ls, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	ms, _ := parquet.NewSink[observability.MetricSample](dir, "metrics", 100)
	ts, _ := parquet.NewSink[observability.SpanSnapshot](dir, "traces", 100)
	rcv := observability.NewReceiver(ls, ms, ts)

	r := chi.NewRouter()
	rcv.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{17, 18, 19, 20, 21, 22, 23, 24}
	parentSpanID := []byte{31, 32, 33, 34, 35, 36, 37, 38}
	const startNs = 1_700_000_000_500_000_000
	const endNs = 1_700_000_000_500_100_000 // +100ms

	req := &collecttrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{
			ScopeSpans: []*tracev1.ScopeSpans{{
				Spans: []*tracev1.Span{{
					TraceId:           traceID,
					SpanId:            spanID,
					ParentSpanId:      parentSpanID,
					Name:              "nyro.gateway.upstream",
					StartTimeUnixNano: startNs,
					EndTimeUnixNano:   endNs,
					Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR},
					Attributes: []*commonv1.KeyValue{{
						Key: "nyro.provider_name",
						Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{
							StringValue: "openai",
						}},
					}},
				}},
			}},
		}},
	}
	body, _ := proto.Marshal(req)
	resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-protobuf" {
		t.Fatalf("content-type=%q", ct)
	}
	// Body must be a valid ExportTraceServiceResponse protobuf (even if empty).
	var tresp collecttrace.ExportTraceServiceResponse
	if rb, rerr := io.ReadAll(resp.Body); rerr != nil {
		t.Fatalf("read body: %v", rerr)
	} else if uerr := proto.Unmarshal(rb, &tresp); uerr != nil {
		t.Fatalf("response body not a valid ExportTraceServiceResponse: %v", uerr)
	}
	rcv.Flush(context.Background())

	got, err := parquet.ReadSince[observability.SpanSnapshot](dir, "traces", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d (%+v)", len(got), got)
	}
	s := got[0]
	wantTrace := hex.EncodeToString(traceID)
	wantSpan := hex.EncodeToString(spanID)
	wantParent := hex.EncodeToString(parentSpanID)
	if s.TraceID != wantTrace {
		t.Errorf("TraceID=%q want %q", s.TraceID, wantTrace)
	}
	if s.SpanID != wantSpan {
		t.Errorf("SpanID=%q want %q", s.SpanID, wantSpan)
	}
	// Also assert the IDs are distinct from each other — a swap would slip past
	// a bare equality check if both fixtures happened to coincide.
	if s.TraceID == s.SpanID {
		t.Errorf("TraceID == SpanID (%q); IDs should differ", s.TraceID)
	}
	if s.ParentSpanID != wantParent {
		t.Errorf("ParentSpanID=%q want %q", s.ParentSpanID, wantParent)
	}
	if s.Name != "nyro.gateway.upstream" {
		t.Errorf("Name=%q want %q", s.Name, "nyro.gateway.upstream")
	}
	if s.StartNs != startNs {
		t.Errorf("StartNs=%d want %d", s.StartNs, startNs)
	}
	if s.EndNs != endNs {
		t.Errorf("EndNs=%d want %d", s.EndNs, endNs)
	}
	if wantDur := int64(endNs - startNs); s.DurationNs != wantDur {
		t.Errorf("DurationNs=%d want %d (End-Start)", s.DurationNs, wantDur)
	}
	if s.StatusCode != int32(tracev1.Status_STATUS_CODE_ERROR) {
		t.Errorf("StatusCode=%d want %d", s.StatusCode, tracev1.Status_STATUS_CODE_ERROR)
	}
	if s.AttrsJSON == "" || s.AttrsJSON == "{}" {
		t.Fatalf("expected non-empty attrs json, got %q", s.AttrsJSON)
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(s.AttrsJSON), &attrs); err != nil {
		t.Fatalf("attrs json not parseable: %q: %v", s.AttrsJSON, err)
	}
	if attrs["nyro.provider_name"] != any("openai") {
		t.Errorf("attrs[nyro.provider_name]=%v want openai (full=%q)", attrs["nyro.provider_name"], s.AttrsJSON)
	}
}

// TestReceiverBadBody exercises the decode-error path: posting bytes that are
// not a valid protobuf wire payload to /v1/logs must yield HTTP 400, not 500
// or 200, and must not panic.
func TestReceiverBadBody(t *testing.T) {
	dir := t.TempDir()
	ls, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	ms, _ := parquet.NewSink[observability.MetricSample](dir, "metrics", 100)
	ts, _ := parquet.NewSink[observability.SpanSnapshot](dir, "traces", 100)
	rcv := observability.NewReceiver(ls, ms, ts)

	r := chi.NewRouter()
	rcv.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/logs", "application/x-protobuf", bytes.NewReader([]byte("not a protobuf")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}

	// Nothing should have been written; flushing and reading back yields zero rows.
	rcv.Flush(context.Background())
	got, err := parquet.ReadSince[observability.LogRecord](dir, "logs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 log rows after a rejected body, got %d", len(got))
	}
}
