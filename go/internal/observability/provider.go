package observability

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

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
// from an ObsConfig, selecting exporters per signal via the exporter registry
// (exporter.go). It is the entry point for gateway-side observability: callers
// use Logger, Meter and Tracer directly, and call Shutdown on graceful exit.
type ObsProvider struct {
	loggerProvider *sdklog.LoggerProvider
	meterProvider  *sdkmetric.MeterProvider
	tracerProvider *sdktrace.TracerProvider

	Logger log.Logger
	Meter  metric.Meter
	Tracer trace.Tracer

	// PromHandler and PromListen are populated when Metrics.Kind ==
	// ExporterKindPrometheus by the prometheus builder (Task 5 — not
	// registered by this package yet). Nil/empty otherwise; the gateway
	// (Task 6) uses PromHandler!=nil to decide whether to start a second
	// HTTP server on PromListen.
	PromHandler http.Handler
	PromListen  string

	shutdownMu sync.Mutex
	shutdown   bool
}

// defaultMetricInterval is used for the metric PeriodicReader export interval
// (and, for otlp traces, the batch timeout) when the signal's Params carries
// no "interval" field — e.g. the stdout exporter, whose field schema
// (exporter.go's stdoutFields) is empty. It matches the otlp exporter's own
// FieldDef default ("5s"), keeping stdout and otlp on the same cadence by
// default as before this task's per-signal split.
const defaultMetricInterval = 5 * time.Second

// parseDuration parses a duration string, returning ok=false for an empty or
// unparsable value so callers can fall back to a default.
func parseDuration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return d, true
}

// requireOTLPEndpoint returns params["endpoint"], failing if it is empty.
// Fail-fast: an otlp exporter without an endpoint would silently drop
// exported signals, so construction must refuse rather than proceed with "".
func requireOTLPEndpoint(signal Signal, params map[string]string) (string, error) {
	endpoint := params["endpoint"]
	if endpoint == "" {
		return "", fmt.Errorf("observability: otlp exporter for signal %q requires an endpoint", signal)
	}
	return endpoint, nil
}

// exporterKindValid reports whether kind is registered for signal per the
// exporter registry (exporter.go's ExportersFor) — the actual source of
// truth for which kinds are valid per signal. Each buildXProvider consults
// this before looking up its local BuilderFunc map, so the registry is
// genuinely checked at construction time, not just trusted implicitly.
// LoadConfig (Task 2) already validates Kind against the same registry when
// config comes from disk, but NewProvider is also callable directly (e.g.
// from tests or future callers) with a hand-built ObsConfig that never went
// through LoadConfig, so this guard is not merely redundant belt-and-braces.
func exporterKindValid(signal Signal, kind ExporterKind) bool {
	for _, def := range ExportersFor(signal) {
		if def.Kind == kind {
			return true
		}
	}
	return false
}

// logsBuilders returns the registry of BuilderFunc values for the "logs"
// signal, keyed by exporter kind. Builders close over ctx (BuilderFunc's
// signature, defined in exporter.go, does not carry one) so exporter
// construction still observes the caller's context, matching the pre-registry
// behavior. Only otlp and stdout are registered; prometheus is not a valid
// logs exporter (see exporter.go's signalExporters).
func logsBuilders(ctx context.Context) map[ExporterKind]BuilderFunc {
	return map[ExporterKind]BuilderFunc{
		ExporterKindOTLP: func(params map[string]string) (any, error) {
			endpoint, err := requireOTLPEndpoint(SignalLogs, params)
			if err != nil {
				return nil, err
			}
			return otlploghttp.New(ctx, otlploghttp.WithEndpointURL(endpoint))
		},
		ExporterKindStdout: func(params map[string]string) (any, error) {
			return stdoutlog.New()
		},
	}
}

// metricsBuilders returns the registry of BuilderFunc values for the
// "metrics" signal. Both registered builders use DELTA temporality (see the
// package-level metric-temporality note below) — this is a fixed property of
// the otlp/stdout engines, not something callers select. prometheus is
// intentionally NOT registered here: it is metrics-valid per
// exporter.ExportersFor, but its builder ships in Task 5 (it uses a pull
// Reader with CUMULATIVE temporality instead of a PeriodicReader — a
// different construction path this task deliberately leaves unbuilt). Until
// then, a metrics config with Kind == "prometheus" fails with a clear "no
// builder registered" error rather than silently doing nothing.
func metricsBuilders(ctx context.Context) map[ExporterKind]BuilderFunc {
	return map[ExporterKind]BuilderFunc{
		ExporterKindOTLP: func(params map[string]string) (any, error) {
			endpoint, err := requireOTLPEndpoint(SignalMetrics, params)
			if err != nil {
				return nil, err
			}
			return otlpmetrichttp.New(ctx,
				otlpmetrichttp.WithEndpointURL(endpoint),
				otlpmetrichttp.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector),
			)
		},
		ExporterKindStdout: func(params map[string]string) (any, error) {
			return stdoutmetric.New(stdoutmetric.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
		},
	}
}

// tracesBuilders returns the registry of BuilderFunc values for the "traces"
// signal.
func tracesBuilders(ctx context.Context) map[ExporterKind]BuilderFunc {
	return map[ExporterKind]BuilderFunc{
		ExporterKindOTLP: func(params map[string]string) (any, error) {
			endpoint, err := requireOTLPEndpoint(SignalTraces, params)
			if err != nil {
				return nil, err
			}
			return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
		},
		ExporterKindStdout: func(params map[string]string) (any, error) {
			return stdouttrace.New()
		},
	}
}

// NewProvider builds the three OTel providers from cfg, one signal at a time,
// by looking up the registered builder for each signal's configured exporter
// Kind and invoking it. Kind == "" (SignalConfig zero value) means the signal
// is disabled: a no-op provider is constructed, matching the previous
// "none"/default switch branch.
//
// Fail-fast: this is checked up front, before any provider is constructed —
// if any signal's Kind is "otlp" but its Params["endpoint"] is empty, an
// error is returned immediately. The gateway must not start in a
// configuration that would drop exported signals silently, and checking
// up front (rather than per-signal during construction) avoids leaving a
// partially-constructed provider (with a live background flush goroutine)
// on the way to returning that error.
//
// Metric temporality: the metric PeriodicReaders use DELTA temporality (not
// the OTel-default Cumulative). Each ~5s export therefore contains only the
// increments since the last export. The admin receiver persists every export
// as a fresh parquet row and AggregateStats/AggregateHourly SUM the
// Value/hist_sum fields — correct for deltas. (Cumulative would double-count:
// R×(N+1)/2.) See also stats_aggregate.go. This is fixed per-engine: otlp and
// stdout are DELTA; prometheus (Task 5) uses its own pull Reader, which is
// CUMULATIVE by construction — the two temporalities coexist because they
// never share a MeterProvider (metrics has exactly one configured exporter).
//
// Global registration: the trace and meter providers are registered globally
// via otel.Set*Provider so libraries instrumented against the OTel API
// (rather than against this struct's fields) also report. Tests construct
// their own providers and do not depend on the global.
func NewProvider(ctx context.Context, cfg ObsConfig) (*ObsProvider, error) {
	signals := []struct {
		signal Signal
		cfg    SignalConfig
	}{
		{SignalLogs, cfg.Logs},
		{SignalMetrics, cfg.Metrics},
		{SignalTraces, cfg.Traces},
	}
	for _, s := range signals {
		if s.cfg.Kind == ExporterKindOTLP {
			if _, err := requireOTLPEndpoint(s.signal, s.cfg.Params); err != nil {
				return nil, err
			}
		}
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("nyro-gateway")))
	if err != nil {
		return nil, err
	}

	lp, err := buildLoggerProvider(ctx, res, cfg.Logs)
	if err != nil {
		return nil, err
	}
	mp, promHandler, promListen, err := buildMeterProvider(ctx, res, cfg.Metrics)
	if err != nil {
		return nil, err
	}
	tp, err := buildTracerProvider(ctx, res, cfg.Traces)
	if err != nil {
		return nil, err
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
		PromHandler:    promHandler,
		PromListen:     promListen,
	}, nil
}

// buildLoggerProvider constructs the LoggerProvider for sc, via the logs
// exporter registry, or a no-op provider when the signal is disabled.
func buildLoggerProvider(ctx context.Context, res *resource.Resource, sc SignalConfig) (*sdklog.LoggerProvider, error) {
	if sc.Kind == "" {
		return sdklog.NewLoggerProvider(sdklog.WithResource(res)), nil
	}

	if !exporterKindValid(SignalLogs, sc.Kind) {
		return nil, fmt.Errorf("observability: exporter kind %q is not registered for signal %q", sc.Kind, SignalLogs)
	}
	builder, ok := logsBuilders(ctx)[sc.Kind]
	if !ok {
		return nil, fmt.Errorf("observability: no builder registered for exporter kind %q (signal %q)", sc.Kind, SignalLogs)
	}
	raw, err := builder(sc.Params)
	if err != nil {
		return nil, err
	}
	exp, ok := raw.(sdklog.Exporter)
	if !ok {
		return nil, fmt.Errorf("observability: logs builder for %q returned unexpected type %T", sc.Kind, raw)
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	), nil
}

// buildMeterProvider constructs the MeterProvider for sc, via the metrics
// exporter registry, or a no-op provider when the signal is disabled. It also
// returns the (currently always nil/empty) PromHandler/PromListen values —
// populated once Task 5 registers a prometheus builder.
func buildMeterProvider(ctx context.Context, res *resource.Resource, sc SignalConfig) (*sdkmetric.MeterProvider, http.Handler, string, error) {
	if sc.Kind == "" {
		return sdkmetric.NewMeterProvider(sdkmetric.WithResource(res)), nil, "", nil
	}

	if !exporterKindValid(SignalMetrics, sc.Kind) {
		return nil, nil, "", fmt.Errorf("observability: exporter kind %q is not registered for signal %q", sc.Kind, SignalMetrics)
	}
	builder, ok := metricsBuilders(ctx)[sc.Kind]
	if !ok {
		return nil, nil, "", fmt.Errorf("observability: no builder registered for exporter kind %q (signal %q)", sc.Kind, SignalMetrics)
	}
	raw, err := builder(sc.Params)
	if err != nil {
		return nil, nil, "", err
	}
	exp, ok := raw.(sdkmetric.Exporter)
	if !ok {
		return nil, nil, "", fmt.Errorf("observability: metrics builder for %q returned unexpected type %T", sc.Kind, raw)
	}

	interval := defaultMetricInterval
	if d, ok := parseDuration(sc.Params["interval"]); ok {
		interval = d
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval))),
	)
	return mp, nil, "", nil
}

// buildTracerProvider constructs the TracerProvider for sc, via the traces
// exporter registry, or a no-op provider when the signal is disabled.
func buildTracerProvider(ctx context.Context, res *resource.Resource, sc SignalConfig) (*sdktrace.TracerProvider, error) {
	if sc.Kind == "" {
		return sdktrace.NewTracerProvider(sdktrace.WithResource(res)), nil
	}

	if !exporterKindValid(SignalTraces, sc.Kind) {
		return nil, fmt.Errorf("observability: exporter kind %q is not registered for signal %q", sc.Kind, SignalTraces)
	}
	builder, ok := tracesBuilders(ctx)[sc.Kind]
	if !ok {
		return nil, fmt.Errorf("observability: no builder registered for exporter kind %q (signal %q)", sc.Kind, SignalTraces)
	}
	raw, err := builder(sc.Params)
	if err != nil {
		return nil, err
	}
	exp, ok := raw.(sdktrace.SpanExporter)
	if !ok {
		return nil, fmt.Errorf("observability: traces builder for %q returned unexpected type %T", sc.Kind, raw)
	}

	// Only apply an explicit batch timeout when the signal's Params actually
	// carry one (i.e. the otlp exporter, whose field schema includes
	// "interval"). stdout has no such field and keeps the SDK's own default,
	// exactly as before this task's per-signal split.
	var batcherOpts []sdktrace.BatchSpanProcessorOption
	if d, ok := parseDuration(sc.Params["interval"]); ok {
		batcherOpts = append(batcherOpts, sdktrace.WithBatchTimeout(d))
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exp, batcherOpts...),
	), nil
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
