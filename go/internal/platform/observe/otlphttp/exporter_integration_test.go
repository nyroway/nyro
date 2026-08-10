package otlphttp_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
	"github.com/nyroway/nyro/go/internal/platform/observe"
	"github.com/nyroway/nyro/go/internal/platform/observe/otlphttp"
	observesqlite "github.com/nyroway/nyro/go/internal/platform/observe/sqlite"
)

func TestOfficialOTelHTTPExportersPersistAllSignals(t *testing.T) {
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "observe.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := observesqlite.New(ctx, db, observesqlite.Options{RetentionInterval: -1})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}
	server := httptest.NewServer(receiver.Handler())
	defer server.Close()

	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", "official-exporter")))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	logExporter, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(server.URL+"/v1/logs"))
	if err != nil {
		t.Fatalf("new log exporter: %v", err)
	}
	loggerProvider := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExporter)))
	var record otellog.Record
	record.SetBody(otellog.StringValue("official log"))
	loggerProvider.Logger("integration").Emit(ctx, record)
	if err := loggerProvider.Shutdown(ctx); err != nil {
		t.Fatalf("logger shutdown: %v", err)
	}

	metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(server.URL+"/v1/metrics"))
	if err != nil {
		t.Fatalf("new metric exporter: %v", err)
	}
	metricReader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(time.Hour))
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(metricReader))
	counter, err := meterProvider.Meter("integration").Int64Counter("official.counter")
	if err != nil {
		t.Fatalf("new counter: %v", err)
	}
	counter.Add(ctx, 1)
	if err := meterProvider.ForceFlush(ctx); err != nil {
		t.Fatalf("metric force flush: %v", err)
	}
	if err := meterProvider.Shutdown(ctx); err != nil {
		t.Fatalf("meter shutdown: %v", err)
	}

	traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(server.URL+"/v1/traces"))
	if err != nil {
		t.Fatalf("new trace exporter: %v", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSyncer(traceExporter))
	_, span := tracerProvider.Tracer("integration").Start(ctx, "official span")
	span.End()
	if err := tracerProvider.Shutdown(ctx); err != nil {
		t.Fatalf("tracer shutdown: %v", err)
	}

	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatalf("receiver shutdown: %v", err)
	}
	logs, err := store.QueryLogs(ctx, observe.LogQuery{Service: "official-exporter"})
	if err != nil || len(logs.Records) != 1 || logs.Records[0].Record.GetBody().GetStringValue() != "official log" {
		t.Fatalf("logs = %#v, err = %v", logs, err)
	}
	metrics, err := store.QueryMetrics(ctx, observe.MetricQuery{Service: "official-exporter", Name: "official.counter"})
	if err != nil || len(metrics.Records) == 0 {
		t.Fatalf("metrics = %#v, err = %v", metrics, err)
	}
	spans, err := store.QuerySpans(ctx, observe.SpanQuery{Service: "official-exporter", Name: "official span"})
	if err != nil || len(spans.Records) != 1 {
		t.Fatalf("spans = %#v, err = %v", spans, err)
	}
}
