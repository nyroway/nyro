package observability_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
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
	if got[0].LabelsJSON == "" || got[0].LabelsJSON == "{}" {
		t.Fatalf("expected non-empty labels json, got %q", got[0].LabelsJSON)
	}
}
