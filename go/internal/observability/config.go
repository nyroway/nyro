package observability

import (
	"fmt"
	"strconv"
)

// SignalConfig is the resolved exporter configuration for a single signal
// (logs, metrics, or traces). Kind == "" means the signal is disabled (no-op
// provider) — there is no "none" sentinel; the zero value SignalConfig{} is
// the disabled state.
type SignalConfig struct {
	Kind   ExporterKind
	Params map[string]string
}

// ObsConfig is the resolved observability configuration. Each signal is
// configured entirely independently — there is no shared/global exporter,
// endpoint, or export interval. DataDir is intentionally NOT populated by
// LoadConfig (see below); it is set by the caller (cmd/admin) after
// LoadConfig returns.
type ObsConfig struct {
	Logs    SignalConfig
	Metrics SignalConfig
	Traces  SignalConfig

	// DataDir is admin-only (the gateway never uses it — it does not persist
	// telemetry). LoadConfig deliberately leaves this at its zero value
	// (""); the caller (cmd/admin, via an --obs-data-dir CLI flag) assigns it
	// after LoadConfig returns.
	DataDir string

	LogsRetentionDays    int
	MetricsRetentionDays int
	TracesRetentionDays  int
}

// signalKeyNames maps each Signal to the key-name segment used in setting
// keys (obs_<name>_exporter, obs_<name>_<kind>_<field>). It happens to equal
// string(signal) for all three signals today, but is kept explicit so a
// future signal whose Signal value diverges from its key name doesn't
// silently break key parsing.
var signalKeyNames = map[Signal]string{
	SignalLogs:    "logs",
	SignalMetrics: "metrics",
	SignalTraces:  "traces",
}

// LoadConfig reads observability settings via get (typically
// s.Settings().Get) and resolves them into an ObsConfig. Each signal is
// resolved independently:
//
//   - obs_<signal>_exporter selects the signal's Kind. Empty/absent means the
//     signal is disabled (SignalConfig{} zero value). A non-empty value that
//     is not a registered exporter for that signal (per ExportersFor) is a
//     validation error.
//   - obs_<signal>_<kind>_<field> supplies each field the selected kind
//     accepts (per ExportersFor's FieldDef list), written to
//     SignalConfig.Params[field] (unprefixed field name). A field left unset
//     falls back to its FieldDef.Default if one exists; if it has neither a
//     setting value nor a default, it is simply absent from Params (fail-fast
//     validation, e.g. otlp requiring endpoint, is a downstream concern for
//     the provider builder, not LoadConfig).
//
// obs_data_dir is no longer read here — see ObsConfig.DataDir. Retention
// settings (obs_<signal>_retention_days) are unchanged.
func LoadConfig(get func(string) (string, error)) (ObsConfig, error) {
	g := func(k string) string { v, _ := get(k); return v }
	ret := func(k string, def int) int {
		if v := g(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
		return def
	}

	cfg := ObsConfig{
		LogsRetentionDays:    ret("obs_logs_retention_days", 7),
		MetricsRetentionDays: ret("obs_metrics_retention_days", 30),
		TracesRetentionDays:  ret("obs_traces_retention_days", 3),
	}

	var err error
	if cfg.Logs, err = loadSignalConfig(g, SignalLogs); err != nil {
		return ObsConfig{}, err
	}
	if cfg.Metrics, err = loadSignalConfig(g, SignalMetrics); err != nil {
		return ObsConfig{}, err
	}
	if cfg.Traces, err = loadSignalConfig(g, SignalTraces); err != nil {
		return ObsConfig{}, err
	}

	return cfg, nil
}

// loadSignalConfig resolves one signal's SignalConfig from settings.
func loadSignalConfig(g func(string) string, signal Signal) (SignalConfig, error) {
	name := signalKeyNames[signal]

	kind := ExporterKind(g(fmt.Sprintf("obs_%s_exporter", name)))
	if kind == "" {
		return SignalConfig{}, nil
	}

	defs := ExportersFor(signal)
	var def *ExporterDef
	for i := range defs {
		if defs[i].Kind == kind {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		return SignalConfig{}, fmt.Errorf("observability: unregistered exporter %q for signal %q", kind, name)
	}

	var params map[string]string
	for _, f := range def.Fields {
		v := g(fmt.Sprintf("obs_%s_%s_%s", name, kind, f.Name))
		if v == "" {
			v = f.Default
		}
		if v == "" {
			continue
		}
		if params == nil {
			params = make(map[string]string, len(def.Fields))
		}
		params[f.Name] = v
	}

	return SignalConfig{Kind: kind, Params: params}, nil
}
