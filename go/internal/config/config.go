package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/platform/state"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

// ── settings ──

type ProxySpec struct {
	RequestTimeout string `yaml:"request_timeout,omitempty"`
	ConnectTimeout string `yaml:"connect_timeout,omitempty"`
	MaxRetries     int    `yaml:"max_retries,omitempty"`
	RetryOnStatus  []int  `yaml:"retry_on_status,omitempty"`
	MaxBodyBytes   int64  `yaml:"max_body_bytes,omitempty"`
}

// StateSpec is the YAML shape of settings.state.
type StateSpec struct {
	Type string `yaml:"type,omitempty"`
	URL  string `yaml:"url,omitempty"`
}

// TelemetryLogsSpec is the YAML shape of settings.telemetry.logs.
// Exporter selects the engine (stdout/otlp); the remaining fields are the
// engine-specific fields flattened directly into the spec (no nested
// per-engine block) — see exporterFieldSetters for the exporter→field
// mapping enforced against internal/telemetry/schema's registry.
type TelemetryLogsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
}

// TelemetryMetricsSpec is the YAML shape of settings.telemetry.metrics.
// Exporter selects the engine (stdout/otlp/prometheus).
type TelemetryMetricsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
	Listen   string `yaml:"listen,omitempty"`
	Path     string `yaml:"path,omitempty"`
}

// TelemetryTracesSpec is the YAML shape of settings.telemetry.traces.
// Exporter selects the engine (stdout/otlp).
type TelemetryTracesSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
}

// TelemetrySpec holds the three per-signal blocks. Each field is a
// pointer so unmarshal can distinguish "block absent from YAML" (nil — the
// signal is silently disabled) from "block present but empty/incomplete"
// (non-nil with a zero Exporter — a validation error, since a present block
// must declare which engine to use).
type TelemetrySpec struct {
	Logs    *TelemetryLogsSpec    `yaml:"logs,omitempty"`
	Metrics *TelemetryMetricsSpec `yaml:"metrics,omitempty"`
	Traces  *TelemetryTracesSpec  `yaml:"traces,omitempty"`
}

// UnmarshalYAML implements custom decoding so that "key present in the YAML
// document" can be distinguished from "key absent" independently of the
// value's YAML kind. Standard struct-tag decoding maps both a fully absent
// `logs:` key and a present-but-null one (bare `logs:`, or `logs:\n  #
// comment`) to the same nil *TelemetryLogsSpec, which loses the
// distinction flattenSettings relies on ("present but empty" must error,
// "absent" must silently disable). Here each of logs/metrics/traces is only
// left nil when its key does not appear in node.Content at all; if the key
// is present, the pointer is always allocated (decoding its value when the
// value is not the YAML null scalar, leaving a zero-value struct — i.e. no
// exporter set — otherwise), matching `logs: {}` behavior.
func (t *TelemetrySpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == 0 || node.Tag == "!!null" {
		// telemetry key absent, or present but null (bare `telemetry:`)
		// — treat as no signal blocks present, same as omitting the key.
		*t = TelemetrySpec{}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("telemetry: expected a mapping, got %v", node.Kind)
	}
	*t = TelemetrySpec{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valNode := node.Content[i], node.Content[i+1]
		switch keyNode.Value {
		case "logs":
			spec := &TelemetryLogsSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("telemetry.logs: %w", err)
				}
			}
			t.Logs = spec
		case "metrics":
			spec := &TelemetryMetricsSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("telemetry.metrics: %w", err)
				}
			}
			t.Metrics = spec
		case "traces":
			spec := &TelemetryTracesSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("telemetry.traces: %w", err)
				}
			}
			t.Traces = spec
		}
	}
	return nil
}

type SettingsSpec struct {
	Proxy     ProxySpec     `yaml:"proxy,omitempty"`
	State     *StateSpec    `yaml:"state,omitempty"`
	Telemetry TelemetrySpec `yaml:"telemetry,omitempty"`
}

// UnmarshalYAML rejects the removed observability key explicitly. yaml.v3's
// default struct decoder ignores unknown fields, which would otherwise turn an
// old config into a silently disabled telemetry pipeline.
func (s *SettingsSpec) UnmarshalYAML(node *yaml.Node) error {
	statePresent := false
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "observability":
				return fmt.Errorf("settings.observability was renamed to settings.telemetry")
			case "state":
				statePresent = true
			}
		}
	}

	type plain SettingsSpec
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	*s = SettingsSpec(decoded)
	if statePresent && s.State == nil {
		s.State = &StateSpec{}
	}
	return nil
}

// ── upstreams ──

type UpstreamProxySpec struct {
	URL string `yaml:"url,omitempty"`
}

type UpstreamSpec struct {
	Name        string            `yaml:"name"`
	Provider    string            `yaml:"provider"`
	Protocol    string            `yaml:"protocol,omitempty"`
	BaseURL     string            `yaml:"base_url,omitempty"`
	Credentials map[string]string `yaml:"credentials,omitempty"`
	Proxy       UpstreamProxySpec `yaml:"proxy,omitempty"`
	// Models is the static model list (mutually exclusive with ModelsURL).
	Models []string `yaml:"models,omitempty"`
	// ModelsURL is a discovery endpoint queried at runtime for this upstream's
	// model list (mutually exclusive with Models). Known providers may omit
	// it and fall back to the preset's default discovery URL; "custom" has no
	// preset, so it must set ModelsURL or Models explicitly.
	ModelsURL string `yaml:"models_url,omitempty"`
	Enabled   *bool  `yaml:"enabled,omitempty"`
}

// validate checks the fields that ApplyTo cannot recover from by falling
// back to a provider preset: provider is required (it is now persisted
// control-plane metadata, not just an input-only template key), models and
// models_url are mutually exclusive, and "custom" — having no preset to fall
// back on — must supply both a base_url and a model source (models or
// models_url).
func (u UpstreamSpec) validate(providers *provider.Catalog) error {
	if strings.TrimSpace(u.Provider) == "" {
		return fmt.Errorf("upstream %q: provider is required", u.Name)
	}
	if len(u.Models) > 0 && u.ModelsURL != "" {
		return fmt.Errorf("upstream %q: models and models_url are mutually exclusive", u.Name)
	}
	if u.Provider == "custom" {
		if u.BaseURL == "" {
			return fmt.Errorf("upstream %q: base_url is required for provider \"custom\"", u.Name)
		}
		if u.ModelsURL == "" && len(u.Models) == 0 {
			return fmt.Errorf("upstream %q: provider \"custom\" requires models or models_url", u.Name)
		}
		return nil
	}
	if providers == nil {
		return fmt.Errorf("upstream %q: provider catalog is required", u.Name)
	}
	if _, ok := providers.Lookup(u.Provider); !ok {
		return fmt.Errorf("upstream %q: unknown provider %q", u.Name, u.Provider)
	}
	return nil
}

// ── routes ──

type RouteUpstreamSpec struct {
	Name     string `yaml:"name"` // upstream name reference
	Model    string `yaml:"model"`
	Weight   int32  `yaml:"weight,omitempty"`
	Priority int32  `yaml:"priority,omitempty"`
	Enabled  *bool  `yaml:"enabled,omitempty"`
}

type RouteSpec struct {
	Model         string              `yaml:"model"`
	Balance       string              `yaml:"balance,omitempty"`
	EnableAuth    bool                `yaml:"enable_auth,omitempty"`
	EnablePayload bool                `yaml:"enable_payload,omitempty"`
	Enabled       *bool               `yaml:"enabled,omitempty"`
	Upstreams     []RouteUpstreamSpec `yaml:"upstreams"`
}

// ── consumers ──

type ConsumerKeySpec struct {
	Name      string `yaml:"name"`
	APIKey    string `yaml:"api_key,omitempty"` // empty = auto-generate
	Enabled   *bool  `yaml:"enabled,omitempty"`
	ExpiresAt string `yaml:"expires_at,omitempty"`
}

type QuotaLimitSpec struct {
	Limit  int64  `yaml:"limit"`
	Window string `yaml:"window"`
}

// BudgetLimitSpec is one spend-budget rule. It is validated and persisted
// only; budgets are not enforced by the proxy in this version.
type BudgetLimitSpec struct {
	Limit    int64  `yaml:"limit"`
	Window   string `yaml:"window"` // s/m/h/d, or "Nmo" for N natural calendar months (e.g. "1mo", "3mo")
	Currency string `yaml:"currency"`
}

// ConsumerConcurrencySpec caps concurrently in-flight requests.
type ConsumerConcurrencySpec struct {
	Limit int64 `yaml:"limit"`
}

type ConsumerQuotasSpec struct {
	// Concurrency caps concurrently in-flight requests; nil/omitted = unlimited.
	Concurrency *ConsumerConcurrencySpec `yaml:"concurrency,omitempty"`
	Requests    []QuotaLimitSpec         `yaml:"requests,omitempty"`
	Tokens      []QuotaLimitSpec         `yaml:"tokens,omitempty"`
	Budgets     []BudgetLimitSpec        `yaml:"budgets,omitempty"`
}

// ConsumerAccessSpec grants a consumer access to models/protocols/source IPs.
// Any empty/omitted field means default-allow for that dimension.
type ConsumerAccessSpec struct {
	Models      []string `yaml:"models,omitempty"`
	Protocols   []string `yaml:"protocols,omitempty"`
	IPAllowlist []string `yaml:"ip_allowlist,omitempty"`
}

// ConsumerLimitsSpec caps per-request resource usage; zero/omitted means no
// limit for that dimension.
type ConsumerLimitsSpec struct {
	MaxInputTokens      int64 `yaml:"max_input_tokens,omitempty"`
	MaxOutputTokens     int64 `yaml:"max_output_tokens,omitempty"`
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes,omitempty"`
}

type ConsumerSpec struct {
	Name     string             `yaml:"name"`
	Enabled  *bool              `yaml:"enabled,omitempty"`
	Metadata map[string]string  `yaml:"metadata,omitempty"`
	Keys     []ConsumerKeySpec  `yaml:"keys"`
	Access   ConsumerAccessSpec `yaml:"access,omitempty"`
	Quotas   ConsumerQuotasSpec `yaml:"quotas,omitempty"`
	Limits   ConsumerLimitsSpec `yaml:"limits,omitempty"`
}

// Config is the standalone YAML configuration.
type Config struct {
	Version   int            `yaml:"version"`
	Settings  SettingsSpec   `yaml:"settings,omitempty"`
	Upstreams []UpstreamSpec `yaml:"upstreams"`
	Routes    []RouteSpec    `yaml:"routes"`
	Consumers []ConsumerSpec `yaml:"consumers"`
}

// LoadYAML reads and parses a standalone config file. "${VAR_NAME}" references
// anywhere in the raw YAML text are expanded from the process environment
// before parsing (the convention documented in the config schema, e.g.
// credentials.api_key: "${OPENAI_API_KEY}"). A reference to an unset
// environment variable expands to an empty string and its name is returned in
// the second result — this deliberately does not fail the load, since not
// every deployment sets every optional variable; callers should log it as a
// warning.
func LoadYAML(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	var missing []string
	seen := map[string]bool{}
	expanded := os.Expand(string(data), func(key string) string {
		v, ok := os.LookupEnv(key)
		if !ok && !seen[key] {
			seen[key] = true
			missing = append(missing, key)
		}
		return v
	})
	var c Config
	if err := yaml.Unmarshal([]byte(expanded), &c); err != nil {
		return nil, missing, fmt.Errorf("parse config: %w", err)
	}
	return &c, missing, nil
}

// ApplyTo is the single YAML→storage conversion path: it seeds upstreams,
// routes (with upstream targets resolved by name), and consumers (keys, route
// grants, quotas) into st. BuildSnapshot reuses this against a throwaway
// in-memory store rather than maintaining a parallel construction path.
func (c *Config) ApplyTo(st storage.Storage, providers *provider.Catalog) error {
	if providers == nil {
		return fmt.Errorf("provider catalog is required")
	}
	upstreamIDs := map[string]string{}
	for _, u := range c.Upstreams {
		if err := u.validate(providers); err != nil {
			return err
		}
		credsJSON, err := json.Marshal(u.Credentials)
		if err != nil {
			return fmt.Errorf("encode credentials for upstream %q: %w", u.Name, err)
		}
		protocolVal, baseURL := strings.TrimSpace(u.Protocol), u.BaseURL
		if protocolVal != "" {
			proto, err := protocol.ParseProtocol(protocolVal)
			if err != nil {
				return fmt.Errorf("upstream %q: %w", u.Name, err)
			}
			protocolVal = proto.String()
		}
		if def, ok := providers.Lookup(u.Provider); ok {
			if protocolVal == "" {
				protocolVal = def.DefaultProtocol
			}
			if baseURL == "" {
				for _, p := range def.Protocols {
					if p.ID == protocolVal {
						baseURL = p.BaseURL
						break
					}
				}
			}
		}

		var modelsJSON []byte
		if len(u.Models) > 0 {
			modelsJSON, err = json.Marshal(u.Models)
			if err != nil {
				return fmt.Errorf("encode models for upstream %q: %w", u.Name, err)
			}
		}
		created, err := st.Upstreams().Create(storage.CreateUpstream{
			Name: u.Name, Provider: u.Provider, Protocol: protocolVal, BaseURL: baseURL,
			CredentialsJSON: credsJSON, ModelsJSON: modelsJSON, ModelsURL: u.ModelsURL,
			ProxyURL: u.Proxy.URL, Enabled: u.Enabled,
		})
		if err != nil {
			return fmt.Errorf("create upstream %q: %w", u.Name, err)
		}
		upstreamIDs[u.Name] = created.ID
	}

	routeModels := map[string]bool{}
	for _, r := range c.Routes {
		targets := make([]storage.CreateRouteUpstream, 0, len(r.Upstreams))
		for _, t := range r.Upstreams {
			uid, ok := upstreamIDs[t.Name]
			if !ok {
				return fmt.Errorf("route %q references unknown upstream %q", r.Model, t.Name)
			}
			targets = append(targets, storage.CreateRouteUpstream{
				UpstreamID: uid, Model: t.Model, Weight: t.Weight, Priority: t.Priority, Enabled: t.Enabled,
			})
		}
		if _, err := st.Routes().Create(storage.CreateRoute{
			Model: r.Model, Balance: storage.ModelBalance(r.Balance), EnableAuth: r.EnableAuth,
			EnablePayload: &r.EnablePayload, Upstreams: targets,
		}); err != nil {
			return fmt.Errorf("create route %q: %w", r.Model, err)
		}
		routeModels[r.Model] = true
	}

	for _, cs := range c.Consumers {
		for _, name := range cs.Access.Models {
			if !routeModels[name] {
				return fmt.Errorf("consumer %q references unknown route %q", cs.Name, name)
			}
		}
		keys := make([]storage.CreateConsumerKey, 0, len(cs.Keys))
		for _, k := range cs.Keys {
			keys = append(keys, storage.CreateConsumerKey{
				Name: k.Name, Token: k.APIKey, Enabled: k.Enabled, ExpiresAt: k.ExpiresAt,
			})
		}
		quotas := consumerQuotas(cs.Quotas)
		var limits *storage.ConsumerLimits
		if cs.Limits != (ConsumerLimitsSpec{}) {
			limits = &storage.ConsumerLimits{
				MaxInputTokens:      cs.Limits.MaxInputTokens,
				MaxOutputTokens:     cs.Limits.MaxOutputTokens,
				MaxRequestBodyBytes: cs.Limits.MaxRequestBodyBytes,
			}
		}
		if _, err := st.Consumers().Create(storage.CreateConsumer{
			Name: cs.Name, Enabled: cs.Enabled, Keys: keys, Routes: cs.Access.Models, Quotas: quotas,
			Metadata: cs.Metadata, Protocols: cs.Access.Protocols, IPAllowlist: cs.Access.IPAllowlist,
			Limits: limits,
		}); err != nil {
			return fmt.Errorf("create consumer %q: %w", cs.Name, err)
		}
	}

	flat, err := flattenSettings(c.Settings)
	if err != nil {
		return err
	}
	for k, v := range flat {
		if err := st.Settings().Set(k, v); err != nil {
			return fmt.Errorf("set setting %q: %w", k, err)
		}
	}

	return nil
}

// consumerQuotas expands the requests/tokens/concurrency/budgets quota shape
// into the flat []CreateConsumerQuota rows the storage layer persists (one
// row per (quota_type, window) pair; concurrency has no window).
func consumerQuotas(q ConsumerQuotasSpec) []storage.CreateConsumerQuota {
	var out []storage.CreateConsumerQuota
	for _, r := range q.Requests {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "requests", QuotaLimit: r.Limit, Window: r.Window})
	}
	for _, t := range q.Tokens {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "tokens", QuotaLimit: t.Limit, Window: t.Window})
	}
	if q.Concurrency != nil {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "concurrency", QuotaLimit: q.Concurrency.Limit})
	}
	for _, b := range q.Budgets {
		out = append(out, storage.CreateConsumerQuota{
			QuotaType: "budget", QuotaLimit: b.Limit, Window: b.Window, Currency: b.Currency,
		})
	}
	return out
}

// flattenSettings expands the nested settings.proxy/telemetry YAML
// shape into the dot-key rows SettingsStore persists. proxy.* uses its own
// dot-key namespace. telemetry.* maps onto the
// obs_<signal>_exporter / obs_<signal>_<engine>_<field> keys
// internal/telemetry's LoadConfig consumes, validated against the exporter
// registry (internal/telemetry/schema.ExportersFor) as described on
// flattenTelemetrySignal.
func flattenSettings(s SettingsSpec) (map[string]string, error) {
	out := map[string]string{}
	setIfNonEmpty := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}

	setIfNonEmpty("proxy.request_timeout", s.Proxy.RequestTimeout)
	setIfNonEmpty("proxy.connect_timeout", s.Proxy.ConnectTimeout)
	if s.Proxy.MaxRetries != 0 {
		out["proxy.max_retries"] = fmt.Sprintf("%d", s.Proxy.MaxRetries)
	}
	if len(s.Proxy.RetryOnStatus) > 0 {
		codes, _ := json.Marshal(s.Proxy.RetryOnStatus)
		out["proxy.retry_on_status"] = string(codes)
	}
	if s.Proxy.MaxBodyBytes > 0 {
		out["proxy.max_body_bytes"] = fmt.Sprintf("%d", s.Proxy.MaxBodyBytes)
	}

	if s.State != nil {
		cfg, err := state.ValidateDeclared(s.State.Type, s.State.URL)
		if err != nil {
			return nil, fmt.Errorf("settings.state: %w", err)
		}
		out[state.SettingTypeKey] = string(cfg.Kind)
		if cfg.Kind == state.KindRedis {
			out[state.SettingURLKey] = cfg.URL
		}
	}

	// A nil block means the signal's YAML key was absent entirely: silently
	// closed, no obs_<signal>_* keys produced (LoadConfig treats a missing
	// obs_<signal>_exporter as disabled). A non-nil block is present in the
	// YAML — even if written as `logs: {}` — and must declare an exporter;
	// flattenTelemetrySignal errors otherwise.
	if l := s.Telemetry.Logs; l != nil {
		if err := flattenTelemetrySignal(out, schema.SignalLogs, l.Exporter, map[string]string{
			"endpoint": l.Endpoint,
			"protocol": l.Protocol,
			"interval": l.Interval,
		}); err != nil {
			return nil, err
		}
	}
	if m := s.Telemetry.Metrics; m != nil {
		if err := flattenTelemetrySignal(out, schema.SignalMetrics, m.Exporter, map[string]string{
			"endpoint": m.Endpoint,
			"protocol": m.Protocol,
			"interval": m.Interval,
			"listen":   m.Listen,
			"path":     m.Path,
		}); err != nil {
			return nil, err
		}
	}
	if t := s.Telemetry.Traces; t != nil {
		if err := flattenTelemetrySignal(out, schema.SignalTraces, t.Exporter, map[string]string{
			"endpoint": t.Endpoint,
			"protocol": t.Protocol,
			"interval": t.Interval,
		}); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// flattenTelemetrySignal validates and flattens one present telemetry
// signal block into obs_<signal>_exporter plus one obs_<signal>_<exporter>_
// <field> key per non-empty field. fields is keyed by FieldDef.Name (e.g.
// "endpoint", "listen") — every key present in fields with a non-empty value
// must belong to the selected exporter's field schema
// (internal/telemetry/schema.ExportersFor), or this returns an error; this is
// what catches YAML like `logs.exporter: stdout` plus a stray
// `logs.endpoint: ...` (stdout has no endpoint field).
//
// exporterName == "" means the block was present in YAML but never set
// exporter (e.g. `logs: {}`) — always an error, since a present block must
// pick an engine.
func flattenTelemetrySignal(out map[string]string, signal schema.Signal, exporterName string, fields map[string]string) error {
	name := string(signal)
	if exporterName == "" {
		return fmt.Errorf("telemetry.%s: exporter is required when the block is present", name)
	}

	defs := schema.ExportersFor(signal)
	var def *schema.ExporterDef
	for i := range defs {
		if string(defs[i].Kind) == exporterName {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		return fmt.Errorf("telemetry.%s: unknown exporter %q", name, exporterName)
	}

	allowed := make(map[string]bool, len(def.Fields))
	for _, f := range def.Fields {
		allowed[f.Name] = true
	}

	out[fmt.Sprintf("obs_%s_exporter", name)] = exporterName
	for field, value := range fields {
		if value == "" {
			continue
		}
		if !allowed[field] {
			return fmt.Errorf("telemetry.%s: field %q is not valid for exporter %q", name, field, exporterName)
		}
		out[fmt.Sprintf("obs_%s_%s_%s", name, exporterName, field)] = value
	}
	return nil
}

// BuildSnapshot constructs the immutable runtime configuration directly from
// YAML, applying the same defaults and reference validation as ApplyTo.
func (c *Config) BuildSnapshot(providers *provider.Catalog) (*configsnapshot.Snapshot, error) {
	if providers == nil {
		return nil, fmt.Errorf("provider catalog is required")
	}
	builder := &configsnapshot.Builder{}
	upstreamIDs := map[string]string{}
	for index, upstream := range c.Upstreams {
		if err := upstream.validate(providers); err != nil {
			return nil, err
		}
		credentials, err := json.Marshal(upstream.Credentials)
		if err != nil {
			return nil, fmt.Errorf("encode credentials for upstream %q: %w", upstream.Name, err)
		}
		protocolValue, baseURL := strings.TrimSpace(upstream.Protocol), upstream.BaseURL
		if protocolValue != "" {
			protocol, err := protocol.ParseProtocol(protocolValue)
			if err != nil {
				return nil, fmt.Errorf("upstream %q: %w", upstream.Name, err)
			}
			protocolValue = protocol.String()
		}
		if definition, ok := providers.Lookup(upstream.Provider); ok {
			if protocolValue == "" {
				protocolValue = definition.DefaultProtocol
			}
			if baseURL == "" {
				for _, protocol := range definition.Protocols {
					if protocol.ID == protocolValue {
						baseURL = protocol.BaseURL
						break
					}
				}
			}
		}
		var models []byte
		if len(upstream.Models) > 0 {
			models, err = json.Marshal(upstream.Models)
			if err != nil {
				return nil, fmt.Errorf("encode models for upstream %q: %w", upstream.Name, err)
			}
		}
		upstreamID := fmt.Sprintf("upstream:%d", index)
		upstreamIDs[upstream.Name] = upstreamID
		builder.SetUpstream(configsnapshot.Upstream{
			ID: upstreamID, Name: upstream.Name, Provider: upstream.Provider, Protocol: protocolValue,
			BaseURL: baseURL, CredentialsJSON: credentials, ModelsJSON: models, ModelsURL: upstream.ModelsURL,
			ProxyURL: upstream.Proxy.URL, Enabled: enabledByDefault(upstream.Enabled),
		})
	}
	routeModels := map[string]bool{}
	for _, route := range c.Routes {
		targets := make([]configsnapshot.RouteTarget, 0, len(route.Upstreams))
		for index, target := range route.Upstreams {
			upstreamID, ok := upstreamIDs[target.Name]
			if !ok {
				return nil, fmt.Errorf("route %q references unknown upstream %q", route.Model, target.Name)
			}
			weight, priority := target.Weight, target.Priority
			if weight == 0 {
				weight = 100
			}
			if priority == 0 {
				priority = 1
			}
			targets = append(targets, configsnapshot.RouteTarget{
				ID: fmt.Sprintf("%s:%d", route.Model, index), RouteID: route.Model, UpstreamID: upstreamID,
				Model: target.Model, Weight: weight, Priority: priority, Enabled: enabledByDefault(target.Enabled),
			})
		}
		enablePayload := route.EnablePayload
		balance := route.Balance
		if balance == "" {
			balance = "weighted"
		}
		builder.SetRoute(configsnapshot.Route{
			ID: route.Model, Model: route.Model, Balance: balance, EnableAuth: route.EnableAuth,
			EnablePayload: &enablePayload, Enabled: true, Upstreams: targets,
		})
		routeModels[route.Model] = true
	}
	for _, consumer := range c.Consumers {
		for _, model := range consumer.Access.Models {
			if !routeModels[model] {
				return nil, fmt.Errorf("consumer %q references unknown route %q", consumer.Name, model)
			}
		}
		quotas := make([]configsnapshot.ConsumerQuota, 0)
		for _, quota := range consumerQuotas(consumer.Quotas) {
			if err := storage.ValidateConsumerQuota(quota); err != nil {
				return nil, fmt.Errorf("create consumer %q: %w", consumer.Name, err)
			}
			quotas = append(quotas, configsnapshot.ConsumerQuota{
				ConsumerID: consumer.Name, QuotaType: quota.QuotaType, QuotaLimit: quota.QuotaLimit,
				Window: quota.Window, Currency: quota.Currency,
			})
		}
		for index, key := range consumer.Keys {
			rawKey := key.APIKey
			if rawKey == "" {
				generated, _, _, err := storage.GenerateKey()
				if err != nil {
					return nil, fmt.Errorf("create consumer %q: %w", consumer.Name, err)
				}
				rawKey = generated
			}
			builder.AddConsumerKey(
				fmt.Sprintf("%s:%d", consumer.Name, index), consumer.Name, key.Name,
				storage.PreviewOf(rawKey), storage.HashKey(rawKey), enabledByDefault(key.Enabled) && enabledByDefault(consumer.Enabled),
				key.ExpiresAt, consumer.Access.Models, quotas,
			)
		}
	}
	settings, err := flattenSettings(c.Settings)
	if err != nil {
		return nil, err
	}
	for key, value := range settings {
		builder.SetSetting(key, value)
	}
	return builder.Build(), nil
}

func enabledByDefault(value *bool) bool {
	return value == nil || *value
}
