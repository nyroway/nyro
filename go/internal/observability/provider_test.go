package observability

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNewProvider_NoneSink constructs a provider with sink "none": no exporters
// are wired, no error is returned, and the Logger/Meter/Tracer fields are usable.
func TestNewProvider_NoneSink(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{Sink: "none"})
	if err != nil {
		t.Fatalf("none sink: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	if p.Logger == nil {
		t.Fatal("none sink: Logger is nil")
	}
	if p.Meter == nil {
		t.Fatal("none sink: Meter is nil")
	}
	if p.Tracer == nil {
		t.Fatal("none sink: Tracer is nil")
	}
}

// TestNewProvider_StdoutSink constructs a provider with sink "stdout": the stdout
// exporters are wired without error.
func TestNewProvider_StdoutSink(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{Sink: "stdout"})
	if err != nil {
		t.Fatalf("stdout sink: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	if p.Logger == nil {
		t.Fatal("stdout sink: Logger is nil")
	}
}

// TestNewProvider_OTLPSinkMissingEndpoint ensures fail-fast: a sink of "otlp"
// with an empty OTLPEndpoint returns an error rather than silently dropping data.
func TestNewProvider_OTLPSinkMissingEndpoint(t *testing.T) {
	_, err := NewProvider(context.Background(), ObsConfig{Sink: "otlp"})
	if err == nil {
		t.Fatal("otlp sink with empty endpoint: want error, got nil")
	}
}

// TestNewProvider_OTLPPerSignalMissingEndpoint ensures the fail-fast rule also
// applies when only a single per-signal override is "otlp" without an endpoint.
func TestNewProvider_OTLPPerSignalMissingEndpoint(t *testing.T) {
	cases := []string{"logs", "metrics", "traces"}
	for _, sig := range cases {
		cfg := ObsConfig{MetricsSink: "none"} // neutralize the others
		cfg.LogsSink = "none"
		cfg.TracesSink = "none"
		switch sig {
		case "logs":
			cfg.LogsSink = "otlp"
		case "metrics":
			cfg.MetricsSink = "otlp"
		case "traces":
			cfg.TracesSink = "otlp"
		}
		if _, err := NewProvider(context.Background(), cfg); err == nil {
			t.Errorf("%s sink=otlp with empty endpoint: want error, got nil", sig)
		}
	}
}

// TestNewProvider_OTLPWithEndpoint constructs an OTLP provider pointed at a
// dummy endpoint. The OTLP HTTP exporter is created lazily; construction against
// an unreachable host must not error at build time (export happens async).
func TestNewProvider_OTLPWithEndpoint(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{
		Sink:         "otlp",
		OTLPEndpoint: "http://127.0.0.1:65535", // unreachable, but exporter builds fine
	})
	if err != nil {
		t.Fatalf("otlp sink with endpoint: unexpected error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
}

// TestShutdownIsIdempotent verifies Shutdown can be called twice without error.
func TestShutdownIsIdempotent(t *testing.T) {
	p, err := NewProvider(context.Background(), ObsConfig{Sink: "none"})
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
// (the one provider.go configures on every metric PeriodicReader), each
// collect emits ONLY the increments recorded since the previous collect — not
// the lifetime running total (which is what the OTel-default Cumulative
// temporality would produce).
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
