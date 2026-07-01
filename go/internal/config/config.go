// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro gateway --config` to run without an admin/DB.
//
// The YAML field names here (providers/models/api_keys) predate the
// config-schema rollout; ApplyTo/BuildSnapshot map them onto the new
// storage.CoreStorage / xds.Snapshot types, but renaming the YAML shape itself
// to upstreams/routes/consumers is a separate, later step.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

type ProviderSpec struct {
	Name     string `yaml:"name"`
	Vendor   string `yaml:"vendor,omitempty"`
	Protocol string `yaml:"protocol"`
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
}

type ModelTargetSpec struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

type ModelSpec struct {
	Name       string            `yaml:"name"`
	EnableAuth bool              `yaml:"enable_auth,omitempty"`
	Targets    []ModelTargetSpec `yaml:"targets"`
}

type APIKeySpec struct {
	Name   string   `yaml:"name"`
	Key    string   `yaml:"key"`
	Models []string `yaml:"models"`
}

type Config struct {
	Providers []ProviderSpec `yaml:"providers"`
	Models    []ModelSpec    `yaml:"models"`
	APIKeys   []APIKeySpec   `yaml:"api_keys"`
}

// LoadYAML reads and parses a standalone config file.
func LoadYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// ApplyTo seeds upstreams, routes (with upstream targets), and consumers (one
// key + route grants each) into storage. References use the YAML name; name→id
// is mapped internally because Create returns opaque generated IDs.
func (c *Config) ApplyTo(st storage.CoreStorage) error {
	upstreamIDs := map[string]string{}
	for _, p := range c.Providers {
		credsJSON, err := json.Marshal(map[string]string{"api_key": p.APIKey})
		if err != nil {
			return fmt.Errorf("encode credentials for provider %q: %w", p.Name, err)
		}
		created, err := st.Upstreams().Create(storage.CreateUpstream{
			Name: p.Name, Provider: p.Vendor, Protocol: p.Protocol, BaseURL: p.BaseURL,
			CredentialsJSON: credsJSON,
		})
		if err != nil {
			return fmt.Errorf("create upstream %q: %w", p.Name, err)
		}
		upstreamIDs[p.Name] = created.ID
	}

	routeModels := map[string]string{} // yaml model name → route model (same value; routes are keyed by Model)
	for _, m := range c.Models {
		targets := make([]storage.CreateRouteUpstream, 0, len(m.Targets))
		for _, t := range m.Targets {
			uid, ok := upstreamIDs[t.Provider]
			if !ok {
				return fmt.Errorf("model %q references unknown provider %q", m.Name, t.Provider)
			}
			targets = append(targets, storage.CreateRouteUpstream{UpstreamID: uid, Model: t.Model})
		}
		if _, err := st.Routes().Create(storage.CreateRoute{
			Model: m.Name, EnableAuth: m.EnableAuth, Upstreams: targets,
		}); err != nil {
			return fmt.Errorf("create route %q: %w", m.Name, err)
		}
		routeModels[m.Name] = m.Name
	}

	for _, k := range c.APIKeys {
		routes := make([]string, 0, len(k.Models))
		for _, name := range k.Models {
			if _, ok := routeModels[name]; !ok {
				return fmt.Errorf("api key %q references unknown model %q", k.Name, name)
			}
			routes = append(routes, name)
		}
		if _, err := st.Consumers().Create(storage.CreateConsumer{
			Name:   k.Name,
			Keys:   []storage.CreateConsumerKey{{Name: k.Name, Token: k.Key}},
			Routes: routes,
		}); err != nil {
			return fmt.Errorf("create consumer for api key %q: %w", k.Name, err)
		}
	}
	return nil
}

// BuildSnapshot constructs an xds.ConfigSnapshot directly from the YAML config
// (no storage round-trip). This is the standalone-mode path: `nyro gateway
// --config` swaps this snapshot into the gateway's cache so config reads work
// without an admin or DB. Settings are empty (the YAML format has no settings
// section in this phase). Stable synthetic IDs are derived from the YAML names
// so bindings resolve consistently.
func (c *Config) BuildSnapshot() (*xds.ConfigSnapshot, error) {
	b := &xds.Snapshot{}

	upstreamIDs := map[string]string{} // yaml name → synthetic id
	for _, p := range c.Providers {
		id := upstreamID(p.Name)
		upstreamIDs[p.Name] = id
		credsJSON, err := json.Marshal(map[string]string{"api_key": p.APIKey})
		if err != nil {
			return nil, fmt.Errorf("encode credentials for provider %q: %w", p.Name, err)
		}
		b.SetUpstream(storage.Upstream{
			ID: id, Name: p.Name, Provider: p.Vendor, Protocol: p.Protocol,
			BaseURL: p.BaseURL, CredentialsJSON: credsJSON, Enabled: true,
		})
	}

	for _, m := range c.Models {
		route := storage.Route{
			ID: routeID(m.Name), Model: m.Name, Balance: storage.BalanceWeighted,
			EnableAuth: m.EnableAuth, Enabled: true,
		}
		for _, t := range m.Targets {
			uid, ok := upstreamIDs[t.Provider]
			if !ok {
				return nil, fmt.Errorf("model %q references unknown provider %q", m.Name, t.Provider)
			}
			route.Upstreams = append(route.Upstreams, storage.RouteUpstream{
				UpstreamID: uid, Model: t.Model, Weight: 1,
			})
		}
		b.SetRoute(route)
	}

	for _, k := range c.APIKeys {
		if k.Key == "" {
			continue
		}
		b.AddConsumerKey(
			consumerKeyID(k.Name), consumerID(k.Name),
			storage.PrefixOf(k.Key), storage.HashKey(k.Key),
			true, "", append([]string(nil), k.Models...), nil,
		)
	}

	return b.Done(), nil
}

// upstreamID derives a stable synthetic upstream id from its YAML name.
func upstreamID(name string) string { return "upstream:" + name }

// routeID derives a stable synthetic route id from its YAML name.
func routeID(name string) string { return "route:" + name }

// consumerID derives a stable synthetic consumer id from its YAML api-key name.
func consumerID(name string) string { return "consumer:" + name }

// consumerKeyID derives a stable synthetic consumer-key id from its YAML name.
func consumerKeyID(name string) string { return "consumer-key:" + name }
