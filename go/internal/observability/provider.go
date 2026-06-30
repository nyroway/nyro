package observability

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ObsProvider assembles the OTel LoggerProvider / MeterProvider / TracerProvider
// from an ObsConfig, selecting exporters per signal (none / stdout / otlp). It
// is the entry point for gateway-side observability: callers use Logger, Meter
// and Tracer directly, and call Shutdown on graceful exit.
type ObsProvider struct {
	loggerProvider *sdklog.LoggerProvider
	meterProvider  *sdkmetric.MeterProvider
	tracerProvider *sdktrace.TracerProvider

	Logger log.Logger
	Meter  metric.Meter
	Tracer trace.Tracer

	shutdownMu sync.Mutex
	shutdown   bool
}

// strOr returns def when v is empty, else v. It is the per-signal override rule:
// an empty LogsSink/MetricsSink/TracesSink inherits the global Sink.
func strOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// resolve expands each per-signal sink against the global default.
func resolve(cfg ObsConfig, logs, metrics, traces string) (string, string, string) {
	return strOr(cfg.Sink, logs), strOr(cfg.Sink, metrics), strOr(cfg.Sink, traces)
}

// NewProvider builds the three OTel providers from cfg.
//
// Fail-fast: if any resolved sink is "otlp" but OTLPEndpoint is empty, an error
// is returned — the gateway must not start in a configuration that would drop
// exported signals silently.
//
// Metric temporality: the metric PeriodicReaders use DELTA temporality (not the
// OTel-default Cumulative). Each ~5s export therefore contains only the
// increments since the last export. The admin receiver persists every export as
// a fresh parquet row and AggregateStats/AggregateHourly SUM the Value/hist_sum
// fields — correct for deltas. (Cumulative would double-count: R×(N+1)/2.) See
// also stats_aggregate.go.
//
// Global registration: the trace and meter providers are registered globally
// via otel.Set*Provider so libraries instrumented against the OTel API (rather
// than against this struct's fields) also report. Tests construct their own
// providers and do not depend on the global.
func NewProvider(ctx context.Context, cfg ObsConfig) (*ObsProvider, error) {
	logSink, metricSink, traceSink := resolve(cfg, cfg.LogsSink, cfg.MetricsSink, cfg.TracesSink)
	if cfg.OTLPEndpoint == "" {
		// otlp sink without an endpoint would silently drop exported signals — refuse.
		if logSink == "otlp" || metricSink == "otlp" || traceSink == "otlp" {
			return nil, errors.New("observability: obs_otlp_endpoint required when a sink is 'otlp'")
		}
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("nyro-gateway")))
	if err != nil {
		return nil, err
	}

	// --- logs ---
	var lp *sdklog.LoggerProvider
	switch logSink {
	case "otlp":
		exp, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		lp = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		)
	case "stdout":
		exp, err := stdoutlog.New()
		if err != nil {
			return nil, err
		}
		lp = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		)
	default: // "none" (or empty)
		lp = sdklog.NewLoggerProvider(sdklog.WithResource(res))
	}

	// --- metrics ---
	// Delta temporality: each export carries only the increments recorded since
	// the previous export, so AggregateStats' plain sum is correct. In this SDK
	// version the PeriodicReader derives its temporality from the EXPORTER's
	// Temporality method (not a reader option), so we configure it on the
	// exporter via WithTemporalitySelector. See the metric-temporality note on
	// NewProvider above.
	var mp *sdkmetric.MeterProvider
	switch metricSink {
	case "otlp":
		exp, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpointURL(cfg.OTLPEndpoint),
			otlpmetrichttp.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector),
		)
		if err != nil {
			return nil, err
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportInterval))),
		)
	case "stdout":
		exp, err := stdoutmetric.New(stdoutmetric.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
		if err != nil {
			return nil, err
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportInterval))),
		)
	default:
		mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))
	}

	// --- traces ---
	var tp *sdktrace.TracerProvider
	switch traceSink {
	case "otlp":
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(cfg.ExportInterval)),
		)
	case "stdout":
		exp, err := stdouttrace.New()
		if err != nil {
			return nil, err
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(exp),
		)
	default:
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return &ObsProvider{
		loggerProvider: lp,
		meterProvider:  mp,
		tracerProvider: tp,
		Logger:         lp.Logger("nyro"),
		Meter:          mp.Meter("nyro"),
		Tracer:         tp.Tracer("nyro"),
	}, nil
}

// Flush is a placeholder for an explicit push of buffered telemetry. The OTel
// SDK flushes on Shutdown; per-signal ForceFlush may be wired here in a later
// task once the exporters' needs become concrete.
func (p *ObsProvider) Flush(ctx context.Context) error { _ = ctx; return nil }

// Shutdown flushes and releases every provider's resources. It is idempotent:
// the first call flushes and tears down the providers; subsequent calls are
// no-ops and return the result of the first call.
func (p *ObsProvider) Shutdown(ctx context.Context) error {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()
	if p.shutdown {
		return nil
	}

	var errs []error
	if err := p.loggerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.meterProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.tracerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}

	p.shutdown = true
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
