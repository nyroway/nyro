package observability

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNewProvider_AllDisabled constructs a provider with every signal
// disabled (Kind == "", the SignalConfig zero value): no exporters are
// wired, no error is returned, and the Logger/Meter/Tracer fields are usable.
func TestNewProvider_AllDisabled(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{})
	if err != nil {
		t.Fatalf("all disabled: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	if p.Logger == nil {
		t.Fatal("all disabled: Logger is nil")
	}
	if p.Meter == nil {
		t.Fatal("all disabled: Meter is nil")
	}
	if p.Tracer == nil {
		t.Fatal("all disabled: Tracer is nil")
	}
	if p.PromHandler != nil {
		t.Fatal("all disabled: PromHandler should be nil")
	}
}

// TestNewProvider_StdoutAllSignals constructs a provider with all three
// signals set to the stdout exporter: the stdout exporters are wired without
// error.
func TestNewProvider_StdoutAllSignals(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{
		Logs:    SignalConfig{Kind: ExporterKindStdout},
		Metrics: SignalConfig{Kind: ExporterKindStdout},
		Traces:  SignalConfig{Kind: ExporterKindStdout},
	})
	if err != nil {
		t.Fatalf("stdout all signals: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	if p.Logger == nil {
		t.Fatal("stdout all signals: Logger is nil")
	}
	if p.Meter == nil {
		t.Fatal("stdout all signals: Meter is nil")
	}
	if p.Tracer == nil {
		t.Fatal("stdout all signals: Tracer is nil")
	}
}

// TestNewProvider_OTLPMissingEndpoint ensures fail-fast: an otlp Kind with no
// "endpoint" Param returns an error rather than silently dropping data.
func TestNewProvider_OTLPMissingEndpoint(t *testing.T) {
	_, err := NewProvider(context.Background(), ObsConfig{
		Logs: SignalConfig{Kind: ExporterKindOTLP},
	})
	if err == nil {
		t.Fatal("otlp logs with empty endpoint: want error, got nil")
	}
}

// TestNewProvider_OTLPPerSignalMissingEndpoint ensures the fail-fast rule
// applies independently to each of the three signals, with the other two
// disabled.
func TestNewProvider_OTLPPerSignalMissingEndpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  func() ObsConfig
	}{
		{"logs", func() ObsConfig { return ObsConfig{Logs: SignalConfig{Kind: ExporterKindOTLP}} }},
		{"metrics", func() ObsConfig { return ObsConfig{Metrics: SignalConfig{Kind: ExporterKindOTLP}} }},
		{"traces", func() ObsConfig { return ObsConfig{Traces: SignalConfig{Kind: ExporterKindOTLP}} }},
	}
	for _, tc := range cases {
		if _, err := NewProvider(context.Background(), tc.cfg()); err == nil {
			t.Errorf("%s Kind=otlp with empty endpoint: want error, got nil", tc.name)
		}
	}
}

// TestNewProvider_OTLPWithEndpoint constructs an OTLP provider pointed at a
// dummy endpoint for all three signals. The OTLP HTTP exporter is created
// lazily; construction against an unreachable host must not error at build
// time (export happens async).
func TestNewProvider_OTLPWithEndpoint(t *testing.T) {
	endpoint := "http://127.0.0.1:65535" // unreachable, but exporter builds fine
	p, err := NewProvider(context.Background(), ObsConfig{
		Logs:    SignalConfig{Kind: ExporterKindOTLP, Params: map[string]string{"endpoint": endpoint}},
		Metrics: SignalConfig{Kind: ExporterKindOTLP, Params: map[string]string{"endpoint": endpoint, "interval": "5s"}},
		Traces:  SignalConfig{Kind: ExporterKindOTLP, Params: map[string]string{"endpoint": endpoint, "interval": "5s"}},
	})
	if err != nil {
		t.Fatalf("otlp with endpoint: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
}

// TestNewProvider_MetricsPrometheusNoBuilder ensures that selecting the
// prometheus exporter for metrics — a signal/kind combination valid per
// exporter.ExportersFor(SignalMetrics) — fails clearly rather than silently
// doing nothing, since no builder is registered for it yet (Task 5).
func TestNewProvider_MetricsPrometheusNoBuilder(t *testing.T) {
	_, err := NewProvider(context.Background(), ObsConfig{
		Metrics: SignalConfig{Kind: ExporterKindPrometheus, Params: map[string]string{"listen": ":9464", "path": "/metrics"}},
	})
	if err == nil {
		t.Fatal("metrics Kind=prometheus: want error (no builder registered), got nil")
	}
}

// TestShutdownIsIdempotent verifies Shutdown can be called twice without error.
func TestShutdownIsIdempotent(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{})
	if err != nil {
		t.Fatalf("shutdown idempotency: setup error: %v", err)
	}
	ctx := context.Background()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: unexpected error: %v", err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown (idempotent): unexpected error: %v", err)
	}
}

// TestDeltaTemporality locks the metric-export temporal assumption that
// AggregateStats/AggregateHourly depend on: with a Delta temporality selector
// (the one provider.go configures on every otlp/stdout metric
// PeriodicReader), each collect emits ONLY the increments recorded since the
// previous collect — not the lifetime running total (which is what the
// OTel-default Cumulative temporality, and the future prometheus Reader,
// would produce).
//
// The contract being asserted: Add(5) → collect shows 5; Add(3) → the SECOND
// collect shows 3 (a cumulative reader would instead show 8, and AggregateStats
// would then double-count across the two windows).
func TestDeltaTemporality(t *testing.T) {
	rdr := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(rdr))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	counter, _ := mp.Meter("nyro").Int64Counter("nyro_requests_total")

	// First window: Add(5), collect → expect 5.
	counter.Add(context.Background(), 5)
	var rm metricdata.ResourceMetrics
	if err := rdr.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect #1: %v", err)
	}
	if got := firstCounterSumValue(t, rm); got != 5 {
		t.Fatalf("collect #1: counter value=%d want 5 (delta temporality)", got)
	}

	// Second window: Add(3), collect → expect 3, NOT 8 (cumulative would give 8).
	counter.Add(context.Background(), 3)
	if err := rdr.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect #2: %v", err)
	}
	if got := firstCounterSumValue(t, rm); got != 3 {
		t.Fatalf("collect #2: counter value=%d want 3 (delta temporality; cumulative would be 8)", got)
	}
}

// firstCounterSumValue extracts the single Sum data point's int64 value from the
// collected ResourceMetrics, failing the test if the shape is unexpected. The
// delta manual reader emits one NumberDataPoint per counter (no attributes here).
func firstCounterSumValue(t *testing.T, rm metricdata.ResourceMetrics) int64 {
	t.Helper()
	if len(rm.ScopeMetrics) != 1 {
		t.Fatalf("expected 1 ScopeMetrics, got %d", len(rm.ScopeMetrics))
	}
	sm := rm.ScopeMetrics[0]
	if len(sm.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(sm.Metrics))
	}
	sum, ok := sm.Metrics[0].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected metricdata.Sum[int64], got %T", sm.Metrics[0].Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(sum.DataPoints))
	}
	return sum.DataPoints[0].Value
}
