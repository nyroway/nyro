package config

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

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
	Models      []string          `yaml:"models,omitempty"`
	Enabled     *bool             `yaml:"enabled,omitempty"`
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

type ConsumerConcurrencySpec struct {
	MaxRequests int64 `yaml:"max_requests"`
}

type ConsumerQuotasSpec struct {
	Requests    []QuotaLimitSpec         `yaml:"requests,omitempty"`
	Tokens      []QuotaLimitSpec         `yaml:"tokens,omitempty"`
	Concurrency *ConsumerConcurrencySpec `yaml:"concurrency,omitempty"`
}

type ConsumerSpec struct {
	Name    string             `yaml:"name"`
	Enabled *bool              `yaml:"enabled,omitempty"`
	Keys    []ConsumerKeySpec  `yaml:"keys"`
	Routes  []string           `yaml:"routes"`
	Quotas  ConsumerQuotasSpec `yaml:"quotas,omitempty"`
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
		credsJSON, err := json.Marshal(u.Credentials)
		if err != nil {
			return fmt.Errorf("encode credentials for upstream %q: %w", u.Name, err)
		}
		var modelsJSON []byte
		if len(u.Models) > 0 {
			modelsJSON, err = json.Marshal(u.Models)
			if err != nil {
				return fmt.Errorf("encode models for upstream %q: %w", u.Name, err)
			}
		}
		created, err := st.Upstreams().Create(storage.CreateUpstream{
			Name: u.Name, Provider: u.Provider, Protocol: u.Protocol, BaseURL: u.BaseURL,
			CredentialsJSON: credsJSON, ModelsJSON: modelsJSON, ProxyURL: u.Proxy.URL,
			Enabled: u.Enabled,
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
		for _, name := range cs.Routes {
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
		if _, err := st.Consumers().Create(storage.CreateConsumer{
			Name: cs.Name, Enabled: cs.Enabled, Keys: keys, Routes: cs.Routes, Quotas: quotas,
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

// consumerQuotas expands the requests/tokens/concurrency quota shape into the
// flat []CreateConsumerQuota rows the storage layer persists (one row per
// (quota_type, window) pair; concurrency has no window).
func consumerQuotas(q ConsumerQuotasSpec) []storage.CreateConsumerQuota {
	var out []storage.CreateConsumerQuota
	for _, r := range q.Requests {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "requests", QuotaLimit: r.Limit, Window: r.Window})
	}
	for _, t := range q.Tokens {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "tokens", QuotaLimit: t.Limit, Window: t.Window})
	}
	if q.Concurrency != nil {
		out = append(out, storage.CreateConsumerQuota{QuotaType: "concurrency", QuotaLimit: q.Concurrency.MaxRequests})
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
