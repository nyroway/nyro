package observability

import (
	"fmt"
	"strconv"

	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

// SignalConfig is the resolved exporter configuration for a single signal
// (logs, metrics, or traces). Kind == "" means the signal is disabled (no-op
// provider) — there is no "none" sentinel; the zero value SignalConfig{} is
// the disabled state.
type SignalConfig struct {
	Kind   schema.ExporterKind
	Params map[string]string
}

// ObsConfig is the resolved observability configuration. Each signal is
// configured entirely independently — there is no shared/global exporter,
// endpoint, or export interval.
type ObsConfig struct {
	Logs    SignalConfig
	Metrics SignalConfig
	Traces  SignalConfig

	LogsRetentionDays    int
	MetricsRetentionDays int
	TracesRetentionDays  int
}

// LoadConfig reads observability settings via get (typically
// s.Settings().Get) and resolves them into an ObsConfig. Each signal is
// resolved independently:
//
//   - obs_<signal>_exporter selects the signal's Kind. Empty/absent means the
//     signal is disabled (SignalConfig{} zero value). A non-empty value that
//     is not a registered exporter for that signal (per schema.ExportersFor)
//     is a validation error.
//   - obs_<signal>_<kind>_<field> supplies each field the selected kind
//     accepts (per schema.ExportersFor's FieldDef list), written to
//     SignalConfig.Params[field] (unprefixed field name). A field left unset
//     falls back to its FieldDef.Default if one exists; if it has neither a
//     setting value nor a default, it is simply absent from Params (fail-fast
//     validation, e.g. otlp requiring endpoint, is a downstream concern for
//     the provider builder, not LoadConfig).
//
// Retention settings (obs_<signal>_retention_days) configure the embedded
// Observe store.
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
	if cfg.Logs, err = loadSignalConfig(g, schema.SignalLogs); err != nil {
		return ObsConfig{}, err
	}
	if cfg.Metrics, err = loadSignalConfig(g, schema.SignalMetrics); err != nil {
		return ObsConfig{}, err
	}
	if cfg.Traces, err = loadSignalConfig(g, schema.SignalTraces); err != nil {
		return ObsConfig{}, err
	}

	return cfg, nil
}

// loadSignalConfig resolves one signal's SignalConfig from settings.
func loadSignalConfig(g func(string) string, signal schema.Signal) (SignalConfig, error) {
	name := string(signal)

	kind := schema.ExporterKind(g(fmt.Sprintf("obs_%s_exporter", name)))
	if kind == "" {
		return SignalConfig{}, nil
	}

	defs := schema.ExportersFor(signal)
	var def *schema.ExporterDef
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
