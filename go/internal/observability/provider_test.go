package observability

import (
	"context"
	"testing"
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
