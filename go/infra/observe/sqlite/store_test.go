package sqlite_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	"github.com/nyroway/nyro/go/infra/observe"
	observesqlite "github.com/nyroway/nyro/go/infra/observe/sqlite"
)

func newStore(t *testing.T, opts observesqlite.Options) *observesqlite.Store {
	t.Helper()
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "observe.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := observesqlite.New(ctx, db, opts)
	if err != nil {
		t.Fatalf("new observe store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	return store
}

func TestStoreAppendsAndQueriesLosslessLog(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, observesqlite.Options{RetentionInterval: -1})

	receivedAt := time.Unix(1_800_000_000, 0)
	request := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key:   "service.name",
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "gateway"}},
		}}},
		ScopeLogs: []*logsv1.ScopeLogs{{
			Scope: &commonv1.InstrumentationScope{Name: "test-scope", Version: "1.0"},
			LogRecords: []*logsv1.LogRecord{{
				TimeUnixNano:   uint64(receivedAt.Add(-time.Second).UnixNano()),
				SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_WARN,
				SeverityText:   "WARN",
				TraceId:        []byte("0123456789abcdef"),
				SpanId:         []byte("01234567"),
				Body:           &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "hello"}},
			}},
		}},
	}}}
	if err := store.Append(ctx, []observe.ExportRequest{{ReceivedAt: receivedAt, Logs: request}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := store.QueryLogs(ctx, observe.LogQuery{Service: "gateway", MinSeverity: int32(logsv1.SeverityNumber_SEVERITY_NUMBER_INFO)})
	if err != nil {
		t.Fatalf("QueryLogs() error = %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("QueryLogs() record count = %d, want 1", len(page.Records))
	}
	got := page.Records[0]
	if got.ReceivedAt != receivedAt || got.Scope.GetName() != "test-scope" || got.Record.GetBody().GetStringValue() != "hello" {
		t.Fatalf("QueryLogs() record = %#v", got)
	}
	if got.Resource.GetAttributes()[0].GetValue().GetStringValue() != "gateway" {
		t.Fatalf("resource was not reconstructed: %#v", got.Resource)
	}
}

func TestStoreAppendsAndQueriesLosslessSpan(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, observesqlite.Options{RetentionInterval: -1})
	receivedAt := time.Unix(1_800_000_100, 0)
	traceID := []byte("0123456789abcdef")
	spanID := []byte("01234567")
	request := &collecttrace.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key:   "service.name",
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "gateway"}},
		}}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Scope: &commonv1.InstrumentationScope{Name: "trace-scope"},
			Spans: []*tracev1.Span{{
				TraceId: traceID, SpanId: spanID, ParentSpanId: []byte("parent01"), Name: "POST /v1/chat",
				StartTimeUnixNano: uint64(receivedAt.Add(-2 * time.Second).UnixNano()),
				EndTimeUnixNano:   uint64(receivedAt.Add(-time.Second).UnixNano()),
				Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK},
				Attributes:        []*commonv1.KeyValue{{Key: "model", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "gpt-test"}}}},
			}},
		}},
	}}}
	if err := store.Append(ctx, []observe.ExportRequest{{ReceivedAt: receivedAt, Traces: request}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := store.QuerySpans(ctx, observe.SpanQuery{Service: "gateway", TraceID: traceID, Name: "POST /v1/chat"})
	if err != nil {
		t.Fatalf("QuerySpans() error = %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("QuerySpans() record count = %d, want 1", len(page.Records))
	}
	got := page.Records[0]
	if !got.ReceivedAt.Equal(receivedAt) || got.Scope.GetName() != "trace-scope" || got.Span.GetAttributes()[0].GetValue().GetStringValue() != "gpt-test" {
		t.Fatalf("QuerySpans() record = %#v", got)
	}
}

func TestStoreAppendsAndQueriesLosslessMetricPoint(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, observesqlite.Options{RetentionInterval: -1})
	receivedAt := time.Unix(1_800_000_200, 0)
	request := &collectmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key:   "service.name",
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "gateway"}},
		}}},
		ScopeMetrics: []*metricsv1.ScopeMetrics{{
			Scope: &commonv1.InstrumentationScope{Name: "metric-scope"},
			Metrics: []*metricsv1.Metric{{
				Name: "request.duration", Unit: "ms", Description: "request latency",
				Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: []*metricsv1.NumberDataPoint{{
					TimeUnixNano: uint64(receivedAt.Add(-time.Second).UnixNano()),
					Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: 12.5},
					Attributes:   []*commonv1.KeyValue{{Key: "route", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "/v1/chat"}}}},
				}}}},
			}},
		}},
	}}}
	if err := store.Append(ctx, []observe.ExportRequest{{ReceivedAt: receivedAt, Metrics: request}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	page, err := store.QueryMetrics(ctx, observe.MetricQuery{Service: "gateway", Name: "request.duration", Type: "gauge"})
	if err != nil {
		t.Fatalf("QueryMetrics() error = %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("QueryMetrics() record count = %d, want 1", len(page.Records))
	}
	got := page.Records[0]
	if !got.ReceivedAt.Equal(receivedAt) || got.PointType != "gauge" || got.DataPointIndex != 0 || got.Metric.GetGauge().GetDataPoints()[0].GetAsDouble() != 12.5 {
		t.Fatalf("QueryMetrics() record = %#v", got)
	}
}

func TestLogQueryUsesEffectiveTimeFallbackAndKeysetCursor(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, observesqlite.Options{RetentionInterval: -1})
	receivedAt := time.Unix(1_800_000_300, 0)
	traceID := []byte("fedcba9876543210")
	spanID := []byte("76543210")
	explicit := &logsv1.LogRecord{TimeUnixNano: uint64(receivedAt.Add(-2 * time.Second).UnixNano()), Body: stringValue("explicit")}
	observed := &logsv1.LogRecord{ObservedTimeUnixNano: uint64(receivedAt.Add(-time.Second).UnixNano()), Body: stringValue("observed")}
	fallback := &logsv1.LogRecord{TraceId: traceID, SpanId: spanID, Body: stringValue("received")}
	unknown := []byte{0x98, 0x06, 0x01}
	fallback.ProtoReflect().SetUnknown(unknown)
	request := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{explicit, observed, fallback}}}}}}
	if err := store.Append(ctx, []observe.ExportRequest{{ReceivedAt: receivedAt, Logs: request}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	first, err := store.QueryLogs(ctx, observe.LogQuery{Limit: 2})
	if err != nil {
		t.Fatalf("first QueryLogs() error = %v", err)
	}
	if len(first.Records) != 2 || first.NextCursor == "" || first.Records[0].Record.GetBody().GetStringValue() != "received" || first.Records[1].Record.GetBody().GetStringValue() != "observed" {
		t.Fatalf("first page = %#v", first)
	}
	if !bytes.Equal(first.Records[0].Record.ProtoReflect().GetUnknown(), unknown) {
		t.Fatalf("unknown protobuf fields = %x, want %x", first.Records[0].Record.ProtoReflect().GetUnknown(), unknown)
	}
	second, err := store.QueryLogs(ctx, observe.LogQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second QueryLogs() error = %v", err)
	}
	if len(second.Records) != 1 || second.NextCursor != "" || second.Records[0].Record.GetBody().GetStringValue() != "explicit" {
		t.Fatalf("second page = %#v", second)
	}
	filtered, err := store.QueryLogs(ctx, observe.LogQuery{TraceID: traceID, SpanID: spanID})
	if err != nil || len(filtered.Records) != 1 || !proto.Equal(filtered.Records[0].Record, fallback) {
		t.Fatalf("filtered records = %#v, err = %v", filtered.Records, err)
	}
}

func stringValue(value string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}
}

func TestStoreRejectsTimestampOutsideSQLiteIntegerRange(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, observesqlite.Options{RetentionInterval: -1})
	request := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{{
		TimeUnixNano: uint64(math.MaxInt64) + 1,
	}}}}}}}
	err := store.Append(ctx, []observe.ExportRequest{{ReceivedAt: time.Now(), Logs: request}})
	if !errors.Is(err, observe.ErrTimestampOutOfRange) {
		t.Fatalf("Append() error = %v, want ErrTimestampOutOfRange", err)
	}
	page, queryErr := store.QueryLogs(ctx, observe.LogQuery{})
	if queryErr != nil || len(page.Records) != 0 {
		t.Fatalf("out-of-range append was not rolled back: %#v, %v", page, queryErr)
	}
}
