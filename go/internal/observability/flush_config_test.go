package observability

import (
	"testing"
	"time"
)

func TestLoadConfig_PerSignalFlushInterval(t *testing.T) {
	// Defaults when unset: each signal falls back to DefaultFlushInterval.
	cfg, err := LoadConfig(func(string) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]time.Duration{
		"logs": cfg.LogsFlushInterval, "metrics": cfg.MetricsFlushInterval, "traces": cfg.TracesFlushInterval,
	} {
		if got != DefaultFlushInterval {
			t.Errorf("%s flush default = %v; want %v", name, got, DefaultFlushInterval)
		}
	}

	// Per-signal overrides are parsed independently; an unparseable/zero value
	// falls back to the default (not zero, which would disable the ticker).
	store := map[string]string{
		"obs_logs_flush_interval":    "30s",
		"obs_metrics_flush_interval": "2s",
		"obs_traces_flush_interval":  "bogus",
	}
	cfg, err = LoadConfig(func(k string) (string, error) { return store[k], nil })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogsFlushInterval != 30*time.Second {
		t.Errorf("logs = %v; want 30s", cfg.LogsFlushInterval)
	}
	if cfg.MetricsFlushInterval != 2*time.Second {
		t.Errorf("metrics = %v; want 2s", cfg.MetricsFlushInterval)
	}
	if cfg.TracesFlushInterval != DefaultFlushInterval {
		t.Errorf("traces (bogus) = %v; want default %v", cfg.TracesFlushInterval, DefaultFlushInterval)
	}
}
