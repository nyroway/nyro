package observability

import (
	"context"
	"testing"
	"time"
)

func TestLoadConfigMapsKnownValues(t *testing.T) {
	cfg := LoadConfig(func(k string) (string, error) {
		switch k {
		case "obs_sink":
			return "otlp", nil
		case "obs_logs_sink":
			return "stdout", nil
		case "obs_metrics_sink":
			return "none", nil
		case "obs_otlp_endpoint":
			return "http://collector:4318", nil
		case "obs_export_interval":
			return "15s", nil
		case "obs_data_dir":
			return "/var/obs", nil
		case "obs_logs_retention_days":
			return "14", nil
		case "obs_metrics_retention_days":
			return "60", nil
		case "obs_traces_retention_days":
			return "5", nil
		}
		return "", nil
	})

	if cfg.Sink != "otlp" {
		t.Errorf("Sink: want otlp, got %q", cfg.Sink)
	}
	if cfg.LogsSink != "stdout" {
		t.Errorf("LogsSink: want stdout, got %q", cfg.LogsSink)
	}
	if cfg.MetricsSink != "none" {
		t.Errorf("MetricsSink: want none, got %q", cfg.MetricsSink)
	}
	if cfg.TracesSink != "" {
		t.Errorf("TracesSink unset: want empty (inherits Sink), got %q", cfg.TracesSink)
	}
	if cfg.OTLPEndpoint != "http://collector:4318" {
		t.Errorf("OTLPEndpoint: got %q", cfg.OTLPEndpoint)
	}
	if cfg.ExportInterval != 15*time.Second {
		t.Errorf("ExportInterval: want 15s, got %v", cfg.ExportInterval)
	}
	if cfg.DataDir != "/var/obs" {
		t.Errorf("DataDir: got %q", cfg.DataDir)
	}
	if cfg.LogsRetentionDays != 14 {
		t.Errorf("LogsRetentionDays: want 14, got %d", cfg.LogsRetentionDays)
	}
	if cfg.MetricsRetentionDays != 60 {
		t.Errorf("MetricsRetentionDays: want 60, got %d", cfg.MetricsRetentionDays)
	}
	if cfg.TracesRetentionDays != 5 {
		t.Errorf("TracesRetentionDays: want 5, got %d", cfg.TracesRetentionDays)
	}
}

func TestLoadConfigDefaultsWhenEmpty(t *testing.T) {
	// get always returns empty string — every default should apply.
	cfg := LoadConfig(func(k string) (string, error) { return "", nil })

	if cfg.Sink != "" {
		t.Errorf("Sink default: want empty, got %q", cfg.Sink)
	}
	if cfg.ExportInterval != 5*time.Second {
		t.Errorf("ExportInterval default: want 5s, got %v", cfg.ExportInterval)
	}
	if cfg.DataDir != "./data/obs" {
		t.Errorf("DataDir default: want ./data/obs, got %q", cfg.DataDir)
	}
	if cfg.LogsRetentionDays != 7 {
		t.Errorf("LogsRetentionDays default: want 7, got %d", cfg.LogsRetentionDays)
	}
	if cfg.MetricsRetentionDays != 30 {
		t.Errorf("MetricsRetentionDays default: want 30, got %d", cfg.MetricsRetentionDays)
	}
	if cfg.TracesRetentionDays != 3 {
		t.Errorf("TracesRetentionDays default: want 3, got %d", cfg.TracesRetentionDays)
	}
}

func TestLoadConfigIgnoresBadRetention(t *testing.T) {
	// A non-numeric / non-positive retention value must fall back to default.
	cfg := LoadConfig(func(k string) (string, error) {
		if k == "obs_logs_retention_days" {
			return "not-a-number", nil
		}
		if k == "obs_metrics_retention_days" {
			return "0", nil
		}
		return "", nil
	})
	if cfg.LogsRetentionDays != 7 {
		t.Errorf("bad logs retention should default to 7, got %d", cfg.LogsRetentionDays)
	}
	if cfg.MetricsRetentionDays != 30 {
		t.Errorf("zero metrics retention should default to 30, got %d", cfg.MetricsRetentionDays)
	}
}

// TestLoadConfigEndpointFollowing mirrors the documented xDS default: the admin
// pushes ONLY obs_otlp_endpoint (no obs_sink / obs_*_sink keys). Every signal's
// resolved sink must follow the endpoint to "otlp" — NOT stay empty/none, which
// would silently drop telemetry (and the fail-fast guard would not catch it
// because no resolved sink reads as "otlp"). This rule lives in LoadConfig so
// BOTH the standalone env path and the xDS cache path apply it consistently.
func TestLoadConfigEndpointFollowing(t *testing.T) {
	cfg := LoadConfig(func(k string) (string, error) {
		if k == "obs_otlp_endpoint" {
			return "http://collector:4318", nil
		}
		return "", nil // no obs_sink, no per-signal sinks
	})

	if cfg.OTLPEndpoint != "http://collector:4318" {
		t.Errorf("OTLPEndpoint: got %q", cfg.OTLPEndpoint)
	}
	if cfg.LogsSink != "otlp" {
		t.Errorf("endpoint-following: LogsSink=%q want otlp (not none)", cfg.LogsSink)
	}
	if cfg.MetricsSink != "otlp" {
		t.Errorf("endpoint-following: MetricsSink=%q want otlp (not none)", cfg.MetricsSink)
	}
	if cfg.TracesSink != "otlp" {
		t.Errorf("endpoint-following: TracesSink=%q want otlp (not none)", cfg.TracesSink)
	}

	// The provider's fail-fast guard must STILL fire when a sink resolves to
	// otlp but the endpoint is somehow empty (here we strip it post-LoadConfig
	// to simulate the guard's own check, proving the resolved sinks are otlp).
	empty := cfg
	empty.OTLPEndpoint = ""
	if _, err := NewProvider(context.Background(), empty); err == nil {
		t.Error("fail-fast guard: otlp sink + empty endpoint must error")
	}
}
