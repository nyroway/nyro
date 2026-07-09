package observability

import (
	"strings"
	"testing"
)

func settingsGetter(kv map[string]string) func(string) (string, error) {
	return func(k string) (string, error) { return kv[k], nil }
}

func TestLoadConfigMapsKnownValues(t *testing.T) {
	cfg, err := LoadConfig(settingsGetter(map[string]string{
		"obs_logs_exporter":          "stdout",
		"obs_metrics_exporter":       "otlp",
		"obs_metrics_otlp_endpoint":  "http://collector:4318",
		"obs_metrics_otlp_protocol":  "grpc",
		"obs_metrics_otlp_interval":  "15s",
		"obs_traces_exporter":        "otlp",
		"obs_traces_otlp_endpoint":   "http://collector:4319",
		"obs_logs_retention_days":    "14",
		"obs_metrics_retention_days": "60",
		"obs_traces_retention_days":  "5",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Logs.Kind != ExporterKindStdout {
		t.Errorf("Logs.Kind: want stdout, got %q", cfg.Logs.Kind)
	}
	if cfg.Logs.Params != nil {
		t.Errorf("Logs.Params: want nil (stdout has no fields), got %v", cfg.Logs.Params)
	}

	if cfg.Metrics.Kind != ExporterKindOTLP {
		t.Errorf("Metrics.Kind: want otlp, got %q", cfg.Metrics.Kind)
	}
	if got := cfg.Metrics.Params["endpoint"]; got != "http://collector:4318" {
		t.Errorf("Metrics.Params[endpoint]: got %q", got)
	}
	if got := cfg.Metrics.Params["protocol"]; got != "grpc" {
		t.Errorf("Metrics.Params[protocol]: got %q (explicit override)", got)
	}
	if got := cfg.Metrics.Params["interval"]; got != "15s" {
		t.Errorf("Metrics.Params[interval]: got %q", got)
	}

	if cfg.Traces.Kind != ExporterKindOTLP {
		t.Errorf("Traces.Kind: want otlp, got %q", cfg.Traces.Kind)
	}
	if got := cfg.Traces.Params["endpoint"]; got != "http://collector:4319" {
		t.Errorf("Traces.Params[endpoint]: got %q", got)
	}
	// protocol not set explicitly for traces -> falls back to FieldDef.Default.
	if got := cfg.Traces.Params["protocol"]; got != "http" {
		t.Errorf("Traces.Params[protocol] default: want http, got %q", got)
	}
	// interval not set explicitly for traces -> falls back to FieldDef.Default.
	if got := cfg.Traces.Params["interval"]; got != "5s" {
		t.Errorf("Traces.Params[interval] default: want 5s, got %q", got)
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

func TestLoadConfigEmptyExporterMeansDisabled(t *testing.T) {
	cfg, err := LoadConfig(settingsGetter(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for name, sc := range map[string]SignalConfig{
		"Logs":    cfg.Logs,
		"Metrics": cfg.Metrics,
		"Traces":  cfg.Traces,
	} {
		if sc.Kind != "" {
			t.Errorf("%s.Kind: want empty (disabled), got %q", name, sc.Kind)
		}
		if sc.Params != nil {
			t.Errorf("%s.Params: want nil, got %v", name, sc.Params)
		}
	}
}

func TestLoadConfigUnregisteredExporterErrors(t *testing.T) {
	// prometheus is not a valid exporter for logs.
	_, err := LoadConfig(settingsGetter(map[string]string{
		"obs_logs_exporter": "prometheus",
	}))
	if err == nil {
		t.Fatal("expected error for unregistered exporter kind, got nil")
	}
	if !strings.Contains(err.Error(), "logs") || !strings.Contains(err.Error(), "prometheus") {
		t.Errorf("error should mention signal and kind, got: %v", err)
	}
}

func TestLoadConfigUnknownExporterKindErrors(t *testing.T) {
	_, err := LoadConfig(settingsGetter(map[string]string{
		"obs_metrics_exporter": "graphite",
	}))
	if err == nil {
		t.Fatal("expected error for unknown exporter kind, got nil")
	}
}

func TestLoadConfigFieldDefaultsFillWhenUnset(t *testing.T) {
	cfg, err := LoadConfig(settingsGetter(map[string]string{
		"obs_metrics_exporter":          "prometheus",
		"obs_metrics_prometheus_listen": "",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Metrics.Params["listen"]; got != ":9464" {
		t.Errorf("Params[listen] default: want :9464, got %q", got)
	}
	if got := cfg.Metrics.Params["path"]; got != "/metrics" {
		t.Errorf("Params[path] default: want /metrics, got %q", got)
	}
}

func TestLoadConfigFieldWithoutDefaultOmittedWhenUnset(t *testing.T) {
	// otlp's endpoint has no Default (Required, but that's not this
	// package's concern) -- if unset it must simply be absent from Params.
	cfg, err := LoadConfig(settingsGetter(map[string]string{
		"obs_traces_exporter": "otlp",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Traces.Params["endpoint"]; ok {
		t.Errorf("Params[endpoint]: want absent (no default, unset), got %q", cfg.Traces.Params["endpoint"])
	}
	// protocol and interval do have defaults, so they should still be present.
	if got := cfg.Traces.Params["protocol"]; got != "http" {
		t.Errorf("Params[protocol] default: want http, got %q", got)
	}
}

func TestLoadConfigRetentionDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadConfig(settingsGetter(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	// Non-numeric / non-positive retention values fall back to default.
	cfg2, err := LoadConfig(settingsGetter(map[string]string{
		"obs_logs_retention_days":    "not-a-number",
		"obs_metrics_retention_days": "0",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.LogsRetentionDays != 7 {
		t.Errorf("bad logs retention should default to 7, got %d", cfg2.LogsRetentionDays)
	}
	if cfg2.MetricsRetentionDays != 30 {
		t.Errorf("zero metrics retention should default to 30, got %d", cfg2.MetricsRetentionDays)
	}
}

func TestLoadConfigDoesNotReadDataDir(t *testing.T) {
	cfg, err := LoadConfig(settingsGetter(map[string]string{
		"obs_data_dir": "/should/be/ignored",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DataDir != "" {
		t.Errorf("DataDir: want empty (LoadConfig must not read obs_data_dir), got %q", cfg.DataDir)
	}
}
