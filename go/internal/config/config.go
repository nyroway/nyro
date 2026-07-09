package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// ── settings ──

type ServerSpec struct {
	Listen  string `yaml:"listen,omitempty"`
	BaseURL string `yaml:"base_url,omitempty"`
}

type ProxySpec struct {
	RequestTimeout string `yaml:"request_timeout,omitempty"`
	ConnectTimeout string `yaml:"connect_timeout,omitempty"`
	MaxRetries     int    `yaml:"max_retries,omitempty"`
	RetryOnStatus  []int  `yaml:"retry_on_status,omitempty"`
}

// ObservabilityLogsSpec is the YAML shape of settings.observability.logs.
// Exporter selects the engine (stdout/otlp); the remaining fields are the
// engine-specific fields flattened directly into the spec (no nested
// per-engine block) — see exporterFieldSetters for the exporter→field
// mapping enforced against internal/observability's registry.
type ObservabilityLogsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
}

// ObservabilityMetricsSpec is the YAML shape of settings.observability.metrics.
// Exporter selects the engine (stdout/otlp/prometheus).
type ObservabilityMetricsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
	Listen   string `yaml:"listen,omitempty"`
	Path     string `yaml:"path,omitempty"`
}

// ObservabilityTracesSpec is the YAML shape of settings.observability.traces.
// Exporter selects the engine (stdout/otlp).
type ObservabilityTracesSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
	Interval string `yaml:"interval,omitempty"`
}

// ObservabilitySpec holds the three per-signal blocks. Each field is a
// pointer so unmarshal can distinguish "block absent from YAML" (nil — the
// signal is silently disabled) from "block present but empty/incomplete"
// (non-nil with a zero Exporter — a validation error, since a present block
// must declare which engine to use).
type ObservabilitySpec struct {
	Logs    *ObservabilityLogsSpec    `yaml:"logs,omitempty"`
	Metrics *ObservabilityMetricsSpec `yaml:"metrics,omitempty"`
	Traces  *ObservabilityTracesSpec  `yaml:"traces,omitempty"`
}

// UnmarshalYAML implements custom decoding so that "key present in the YAML
// document" can be distinguished from "key absent" independently of the
// value's YAML kind. Standard struct-tag decoding maps both a fully absent
// `logs:` key and a present-but-null one (bare `logs:`, or `logs:\n  #
// comment`) to the same nil *ObservabilityLogsSpec, which loses the
// distinction flattenSettings relies on ("present but empty" must error,
// "absent" must silently disable). Here each of logs/metrics/traces is only
// left nil when its key does not appear in node.Content at all; if the key
// is present, the pointer is always allocated (decoding its value when the
// value is not the YAML null scalar, leaving a zero-value struct — i.e. no
// exporter set — otherwise), matching `logs: {}` behavior.
func (o *ObservabilitySpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == 0 || node.Tag == "!!null" {
		// observability key absent, or present but null (bare `observability:`)
		// — treat as no signal blocks present, same as omitting the key.
		*o = ObservabilitySpec{}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("observability: expected a mapping, got %v", node.Kind)
	}
	*o = ObservabilitySpec{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valNode := node.Content[i], node.Content[i+1]
		switch keyNode.Value {
		case "logs":
			spec := &ObservabilityLogsSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("observability.logs: %w", err)
				}
			}
			o.Logs = spec
		case "metrics":
			spec := &ObservabilityMetricsSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("observability.metrics: %w", err)
				}
			}
			o.Metrics = spec
		case "traces":
			spec := &ObservabilityTracesSpec{}
			if valNode.Tag != "!!null" {
				if err := valNode.Decode(spec); err != nil {
					return fmt.Errorf("observability.traces: %w", err)
				}
			}
			o.Traces = spec
		}
	}
	return nil
}

type SettingsSpec struct {
	Server        ServerSpec        `yaml:"server,omitempty"`
	Proxy         ProxySpec         `yaml:"proxy,omitempty"`
	Observability ObservabilitySpec `yaml:"observability,omitempty"`
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
func (u UpstreamSpec) validate() error {
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
	if _, ok := provider.Lookup(u.Provider); !ok {
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
func (c *Config) ApplyTo(st storage.Storage) error {
	upstreamIDs := map[string]string{}
	for _, u := range c.Upstreams {
		if err := u.validate(); err != nil {
			return err
		}
		credsJSON, err := json.Marshal(u.Credentials)
		if err != nil {
			return fmt.Errorf("encode credentials for upstream %q: %w", u.Name, err)
		}
		protocolVal, baseURL := strings.TrimSpace(u.Protocol), u.BaseURL
		if protocolVal != "" {
			proto, err := ids.ParseProtocol(protocolVal)
			if err != nil {
				return fmt.Errorf("upstream %q: %w", u.Name, err)
			}
			protocolVal = proto.String()
		}
		if def, ok := provider.Lookup(u.Provider); ok {
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

// flattenSettings expands the nested settings.server/proxy/observability YAML
// shape into the dot-key rows SettingsStore persists. server.*/proxy.* use
// their own dot-key namespace. observability.* maps onto the
// obs_<signal>_exporter / obs_<signal>_<engine>_<field> keys
// internal/observability's LoadConfig consumes, validated against the
// exporter registry (internal/observability.ExportersFor) as described on
// flattenObservabilitySignal.
func flattenSettings(s SettingsSpec) (map[string]string, error) {
	out := map[string]string{}
	setIfNonEmpty := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}

	setIfNonEmpty("server.listen", s.Server.Listen)
	setIfNonEmpty("server.base_url", s.Server.BaseURL)

	setIfNonEmpty("proxy.request_timeout", s.Proxy.RequestTimeout)
	setIfNonEmpty("proxy.connect_timeout", s.Proxy.ConnectTimeout)
	if s.Proxy.MaxRetries != 0 {
		out["proxy.max_retries"] = fmt.Sprintf("%d", s.Proxy.MaxRetries)
	}
	if len(s.Proxy.RetryOnStatus) > 0 {
		codes, _ := json.Marshal(s.Proxy.RetryOnStatus)
		out["proxy.retry_on_status"] = string(codes)
	}

	// A nil block means the signal's YAML key was absent entirely: silently
	// closed, no obs_<signal>_* keys produced (LoadConfig treats a missing
	// obs_<signal>_exporter as disabled). A non-nil block is present in the
	// YAML — even if written as `logs: {}` — and must declare an exporter;
	// flattenObservabilitySignal errors otherwise.
	if l := s.Observability.Logs; l != nil {
		if err := flattenObservabilitySignal(out, observability.SignalLogs, l.Exporter, map[string]string{
			"endpoint": l.Endpoint,
			"protocol": l.Protocol,
			"interval": l.Interval,
		}); err != nil {
			return nil, err
		}
	}
	if m := s.Observability.Metrics; m != nil {
		if err := flattenObservabilitySignal(out, observability.SignalMetrics, m.Exporter, map[string]string{
			"endpoint": m.Endpoint,
			"protocol": m.Protocol,
			"interval": m.Interval,
			"listen":   m.Listen,
			"path":     m.Path,
		}); err != nil {
			return nil, err
		}
	}
	if t := s.Observability.Traces; t != nil {
		if err := flattenObservabilitySignal(out, observability.SignalTraces, t.Exporter, map[string]string{
			"endpoint": t.Endpoint,
			"protocol": t.Protocol,
			"interval": t.Interval,
		}); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// flattenObservabilitySignal validates and flattens one present observability
// signal block into obs_<signal>_exporter plus one obs_<signal>_<exporter>_
// <field> key per non-empty field. fields is keyed by FieldDef.Name (e.g.
// "endpoint", "listen") — every key present in fields with a non-empty value
// must belong to the selected exporter's field schema
// (internal/observability.ExportersFor), or this returns an error; this is
// what catches YAML like `logs.exporter: stdout` plus a stray
// `logs.endpoint: ...` (stdout has no endpoint field).
//
// exporterName == "" means the block was present in YAML but never set
// exporter (e.g. `logs: {}`) — always an error, since a present block must
// pick an engine.
func flattenObservabilitySignal(out map[string]string, signal observability.Signal, exporterName string, fields map[string]string) error {
	name := string(signal)
	if exporterName == "" {
		return fmt.Errorf("observability.%s: exporter is required when the block is present", name)
	}

	defs := observability.ExportersFor(signal)
	var def *observability.ExporterDef
	for i := range defs {
		if string(defs[i].Kind) == exporterName {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		return fmt.Errorf("observability.%s: unknown exporter %q", name, exporterName)
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
			return fmt.Errorf("observability.%s: field %q is not valid for exporter %q", name, field, exporterName)
		}
		out[fmt.Sprintf("obs_%s_%s_%s", name, exporterName, field)] = value
	}
	return nil
}

// BuildSnapshot constructs a configsync.ConfigSnapshot directly from the YAML
// config (no persistent storage). This is the standalone-mode path: `nyro
// gateway --config` swaps this snapshot into the gateway's cache so config
// reads work without an admin or DB. It seeds a throwaway in-memory backend
// via the same ApplyTo used by persistent backends and reads the snapshot
// back through configsync.LoadFromStorage — there is no separate
// synthetic-ID construction path to keep in sync.
func (c *Config) BuildSnapshot() (*configsync.ConfigSnapshot, error) {
	tmp := memory.New()
	if err := c.ApplyTo(tmp.Storage()); err != nil {
		return nil, err
	}
	return configsync.LoadFromStorage(tmp.Storage())
}
