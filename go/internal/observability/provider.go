package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
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
	// ExporterKindPrometheus by the prometheus builder in metricsBuilders.
	// Nil/empty otherwise; the gateway (Task 6) uses PromHandler!=nil to
	// decide whether to start a second HTTP server on PromListen.
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

// Per-signal default OTLP/HTTP request paths, matching the OTLP spec (and the
// paths the admin receiver mounts in internal/observability/receiver.go).
const (
	otlpLogsPath    = "/v1/logs"
	otlpMetricsPath = "/v1/metrics"
	otlpTracesPath  = "/v1/traces"
)

// otlpSignalURL normalizes a configured OTLP endpoint for a specific signal.
// The endpoint stored in settings (and seeded by the admin — see
// cmd/admin.seedDefaultObsEndpoint) is a BASE url like "http://127.0.0.1:19531"
// shared across logs/metrics/traces; it carries no per-signal path.
//
// This matters because otlploghttp/otlpmetrichttp/otlptracehttp's
// WithEndpointURL uses the URL's path verbatim and does NOT append the default
// "/v1/<signal>" when it is empty — a path-less base URL makes the exporter POST
// to "/", which the receiver 404s. (WithEndpoint, the host:port form, would
// append the default, but our config carries a full scheme://host so
// WithEndpointURL is the natural fit.) So we append the per-signal default path
// ourselves here, mirroring the OTEL_EXPORTER_OTLP_ENDPOINT base-endpoint
// convention. An endpoint that already carries an explicit path (a user pointing
// at a custom collector route) is left untouched.
func otlpSignalURL(endpoint, defaultPath string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		// Malformed or scheme-less: leave it for WithEndpointURL to handle/surface
		// rather than silently rewriting into something unexpected.
		return endpoint
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = defaultPath
	}
	return u.String()
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
			return otlploghttp.New(ctx, otlploghttp.WithEndpointURL(otlpSignalURL(endpoint, otlpLogsPath)))
		},
		ExporterKindStdout: func(params map[string]string) (any, error) {
			return stdoutlog.New()
		},
	}
}

// defaultPrometheusListen is the fallback listen address used by the
// prometheus builder when Params["listen"] is empty. It matches
// exporter.go's prometheusFields "listen" FieldDef Default (":9464").
// LoadConfig already fills this default from the registry when config comes
// from disk, but NewProvider/the builder are also callable directly with a
// hand-built SignalConfig that never went through LoadConfig (e.g.
// SignalConfig{Kind: "prometheus"} with no Params), so the builder defends
// against an empty listen value itself rather than trusting the caller.
const defaultPrometheusListen = ":9464"

// promReaderResult is what the prometheus BuilderFunc entry in
// metricsBuilders returns, instead of an sdkmetric.Exporter like the
// otlp/stdout entries. Prometheus is a pull-model reader (scraped, not
// pushed), so it has no PeriodicReader/interval and no separate "exporter"
// object to wrap — buildMeterProvider type-switches on this to take a
// different construction path (sdkmetric.WithReader(Reader) directly) and to
// thread Handler/Listen out to ObsProvider.
type promReaderResult struct {
	Reader  sdkmetric.Reader
	Handler http.Handler
	Listen  string
}

// metricsBuilders returns the registry of BuilderFunc values for the
// "metrics" signal. The otlp/stdout builders use DELTA temporality (see the
// package-level metric-temporality note below) — this is a fixed property of
// those engines, not something callers select. The prometheus builder
// constructs an independent prometheus.Registry (never the global
// DefaultRegisterer, to avoid colliding with other Prometheus
// instrumentation in the process) plus an otelprom.Reader wired to it, and
// returns both the reader and an http.Handler serving that registry via
// promReaderResult. It is CUMULATIVE by construction (the reader type has no
// temporality selector to set) — deliberately not wrapped in
// stdoutmetric/otlpmetrichttp's WithTemporalitySelector(Delta), which does
// not apply to this reader type anyway.
func metricsBuilders(ctx context.Context) map[ExporterKind]BuilderFunc {
	return map[ExporterKind]BuilderFunc{
		ExporterKindOTLP: func(params map[string]string) (any, error) {
			endpoint, err := requireOTLPEndpoint(SignalMetrics, params)
			if err != nil {
				return nil, err
			}
			return otlpmetrichttp.New(ctx,
				otlpmetrichttp.WithEndpointURL(otlpSignalURL(endpoint, otlpMetricsPath)),
				otlpmetrichttp.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector),
			)
		},
		ExporterKindStdout: func(params map[string]string) (any, error) {
			return stdoutmetric.New(stdoutmetric.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
		},
		ExporterKindPrometheus: func(params map[string]string) (any, error) {
			reg := prometheus.NewRegistry()
			reader, err := otelprom.New(otelprom.WithRegisterer(reg))
			if err != nil {
				return nil, err
			}
			listen := params["listen"]
			if listen == "" {
				listen = defaultPrometheusListen
			}
			return promReaderResult{
				Reader:  reader,
				Handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
				Listen:  listen,
			}, nil
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
			return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(otlpSignalURL(endpoint, otlpTracesPath)))
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

	// Honor the configured "interval" (the WebUI's Export Interval) as the batch
	// processor's export cadence, matching how metrics (PeriodicReader interval)
	// and traces (BatchSpanProcessor timeout) consume the same field. stdout has
	// no interval field, so it keeps the SDK's own default.
	var procOpts []sdklog.BatchProcessorOption
	if d, ok := parseDuration(sc.Params["interval"]); ok {
		procOpts = append(procOpts, sdklog.WithExportInterval(d))
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp, procOpts...)),
	), nil
}

// buildMeterProvider constructs the MeterProvider for sc, via the metrics
// exporter registry, or a no-op provider when the signal is disabled. For
// prometheus (a pull-model reader), it returns the reader's Handler/Listen so
// NewProvider can populate ObsProvider.PromHandler/PromListen; for otlp/stdout
// (push-model exporters) it returns nil/"" as before.
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

	// prometheus is a pull-model Reader, not a push-model Exporter: it takes
	// a different construction path (WithReader directly, no
	// PeriodicReader/interval) and threads Handler/Listen out to the caller.
	if pr, ok := raw.(promReaderResult); ok {
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(pr.Reader),
		)
		return mp, pr.Handler, pr.Listen, nil
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
