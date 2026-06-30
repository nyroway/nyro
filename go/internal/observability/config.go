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
	Sink           string // global default: none|stdout|otlp
	LogsSink       string // per-signal override ("" → Sink)
	MetricsSink    string
	TracesSink     string
	OTLPEndpoint   string // e.g. "http://127.0.0.1:19531"
	ExportInterval time.Duration

	// Admin (sink) side.
	DataDir              string
	LogsRetentionDays    int
	MetricsRetentionDays int
	TracesRetentionDays  int
}

// LoadConfig reads observability settings via get (typically s.Settings().Get).
// Empty / invalid strings resolve to documented defaults.
//
// Endpoint-following: when obs_otlp_endpoint is set and a per-signal sink is
// empty (i.e. neither pinned nor defaulted), that signal resolves to "otlp" —
// the documented xDS default where the admin pushes only obs_otlp_endpoint.
// This rule is applied HERE (not in each call site) so BOTH the standalone YAML
// path (env-driven) and the xDS path (cache-driven) follow the endpoint
// consistently. The per-signal default still wins for an unset endpoint (logs→
// stdout in standalone; otherwise the global Sink or "none").
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
	cfg := ObsConfig{
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
	applyEndpointFollowing(&cfg)
	return cfg
}

// applyEndpointFollowing resolves every empty per-signal sink to "otlp" when an
// OTLP endpoint is configured AND there is no global Sink to inherit. It mutates
// cfg in place.
//
// The "no global Sink" guard matters: an empty per-signal sink normally inherits
// the global Sink via provider.resolve/strOr. When obs_sink is set (e.g. the
// standalone env sets Sink=otlp, or a snapshot pins obs_sink), the empty
// per-signal sinks already inherit correctly and must NOT be force-set here
// (that would break the inherit contract asserted by config tests). The
// endpoint-following rule is only needed when NOTHING else has configured a
// sink — exactly the xDS default of "admin pushed obs_otlp_endpoint only".
//
// The fail-fast guard in NewProvider still catches an "otlp" sink with an empty
// endpoint.
func applyEndpointFollowing(cfg *ObsConfig) {
	if cfg.OTLPEndpoint == "" || cfg.Sink != "" {
		return
	}
	if cfg.LogsSink == "" {
		cfg.LogsSink = "otlp"
	}
	if cfg.MetricsSink == "" {
		cfg.MetricsSink = "otlp"
	}
	if cfg.TracesSink == "" {
		cfg.TracesSink = "otlp"
	}
}
