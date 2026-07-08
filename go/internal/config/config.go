package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/xds"
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

type ObservabilityLogsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
}

type ObservabilityMetricsSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Path     string `yaml:"path,omitempty"`
}

type ObservabilityTracesSpec struct {
	Exporter string `yaml:"exporter,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
}

type ObservabilitySpec struct {
	Logs    ObservabilityLogsSpec    `yaml:"logs,omitempty"`
	Metrics ObservabilityMetricsSpec `yaml:"metrics,omitempty"`
	Traces  ObservabilityTracesSpec  `yaml:"traces,omitempty"`
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

	for k, v := range flattenSettings(c.Settings) {
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
// shape into the dot-key rows SettingsStore persists. observability.* maps
// onto the existing obs_* keys observability/config.go already consumes;
// server.*/proxy.* use their own dot-key namespace.
func flattenSettings(s SettingsSpec) map[string]string {
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

	setIfNonEmpty("obs_logs_sink", s.Observability.Logs.Exporter)
	setIfNonEmpty("obs_metrics_sink", s.Observability.Metrics.Exporter)
	setIfNonEmpty("obs_metrics_path", s.Observability.Metrics.Path)
	setIfNonEmpty("obs_traces_sink", s.Observability.Traces.Exporter)
	setIfNonEmpty("obs_otlp_endpoint", s.Observability.Traces.Endpoint)
	setIfNonEmpty("obs_traces_protocol", s.Observability.Traces.Protocol)

	return out
}

// BuildSnapshot constructs an xds.ConfigSnapshot directly from the YAML config
// (no persistent storage). This is the standalone-mode path: `nyro gateway
// --config` swaps this snapshot into the gateway's cache so config reads work
// without an admin or DB. It seeds a throwaway in-memory backend via the same
// ApplyTo used by persistent backends and reads the snapshot back through
// xds.LoadFromStorage — there is no separate synthetic-ID construction path to
// keep in sync.
func (c *Config) BuildSnapshot() (*xds.ConfigSnapshot, error) {
	tmp := memory.New()
	if err := c.ApplyTo(tmp.Storage()); err != nil {
		return nil, err
	}
	return xds.LoadFromStorage(tmp.Storage())
}
