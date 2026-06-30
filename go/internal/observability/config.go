package observability

import (
	"strconv"
	"time"
)

// ObsConfig is the resolved observability configuration. It spans both sides:
// the gateway exporter (Sink/OTLP) and the admin persistence (DataDir +
// retention). It is produced by LoadConfig from settings.
type ObsConfig struct {
	// Gateway (exporter) side.
	Sink         string // global default: none|stdout|otlp
	LogsSink     string // per-signal override ("" → Sink)
	MetricsSink  string
	TracesSink   string
	OTLPEndpoint string // e.g. "http://127.0.0.1:19531"
	ExportInterval time.Duration

	// Admin (sink) side.
	DataDir              string
	LogsRetentionDays    int
	MetricsRetentionDays int
	TracesRetentionDays  int
}

// LoadConfig reads observability settings via get (typically s.Settings().Get).
// Empty / invalid strings resolve to documented defaults.
func LoadConfig(get func(string) (string, error)) ObsConfig {
	g := func(k string) string { v, _ := get(k); return v }
	itv, _ := time.ParseDuration(g("obs_export_interval"))
	if itv <= 0 {
		itv = 5 * time.Second
	}
	dataDir := g("obs_data_dir")
	if dataDir == "" {
		dataDir = "./data/obs"
	}
	ret := func(k string, def int) int {
		if v := g(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
		return def
	}
	return ObsConfig{
		Sink:                 g("obs_sink"),
		LogsSink:             g("obs_logs_sink"),
		MetricsSink:          g("obs_metrics_sink"),
		TracesSink:           g("obs_traces_sink"),
		OTLPEndpoint:         g("obs_otlp_endpoint"),
		ExportInterval:       itv,
		DataDir:              dataDir,
		LogsRetentionDays:    ret("obs_logs_retention_days", 7),
		MetricsRetentionDays: ret("obs_metrics_retention_days", 30),
		TracesRetentionDays:  ret("obs_traces_retention_days", 3),
	}
}
