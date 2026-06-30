// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro gateway --config` to run without an admin/DB.
package config

import (
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

// ApplyTo seeds providers, models (with provider binding), and api_keys (with
// model binding) into storage. References use the YAML name; name→id is mapped
// internally because Create returns opaque generated IDs.
func (c *Config) ApplyTo(st storage.Storage) error {
	providerIDs := map[string]string{}
	for _, p := range c.Providers {
		created, err := st.Providers().Create(storage.CreateProvider{
			Name: p.Name, Vendor: p.Vendor, Protocol: p.Protocol, BaseURL: p.BaseURL, APIKey: p.APIKey,
		})
		if err != nil {
			return fmt.Errorf("create provider %q: %w", p.Name, err)
		}
		providerIDs[p.Name] = created.ID
	}

	modelIDs := map[string]string{}
	for _, m := range c.Models {
		targets := make([]storage.CreateModelBackend, 0, len(m.Targets))
		for _, t := range m.Targets {
			pid, ok := providerIDs[t.Provider]
			if !ok {
				return fmt.Errorf("model %q references unknown provider %q", m.Name, t.Provider)
			}
			targets = append(targets, storage.CreateModelBackend{ProviderID: pid, Model: t.Model})
		}
		created, err := st.Models().Create(storage.CreateModel{
			Name: m.Name, EnableAuth: m.EnableAuth, Targets: targets,
		})
		if err != nil {
			return fmt.Errorf("create model %q: %w", m.Name, err)
		}
		modelIDs[m.Name] = created.ID
	}

	for _, k := range c.APIKeys {
		mids := make([]string, 0, len(k.Models))
		for _, name := range k.Models {
			mid, ok := modelIDs[name]
			if !ok {
				return fmt.Errorf("api key %q references unknown model %q", k.Name, name)
			}
			mids = append(mids, mid)
		}
		if _, err := st.APIKeys().Create(storage.CreateApiKey{
			Name: k.Name, Token: k.Key, ModelIDs: mids,
		}); err != nil {
			return fmt.Errorf("create api key %q: %w", k.Name, err)
		}
	}
	return nil
}

// BuildSnapshot constructs an xds.ConfigSnapshot directly from the YAML config
// (no storage round-trip). This is the standalone-mode path: `nyro gateway
// --config` swaps this snapshot into the gateway's cache so config reads work
// without an admin or DB. Providers/models/apikeys are keyed the same way the
// in-memory storage keys them; settings are empty (the YAML format has no
// settings section in this phase). Stable synthetic IDs are derived from the
// YAML names so bindings resolve consistently.
func (c *Config) BuildSnapshot() (*xds.ConfigSnapshot, error) {
	b := &xds.Snapshot{}

	providerIDs := map[string]string{} // yaml name → synthetic id
	for _, p := range c.Providers {
		id := providerID(p.Name)
		providerIDs[p.Name] = id
		b.SetProvider(storage.Provider{
			ID: id, Name: p.Name, Vendor: p.Vendor, Protocol: p.Protocol,
			BaseURL: p.BaseURL, APIKey: p.APIKey, AuthMode: "apikey", IsEnabled: true,
		})
	}

	modelIDs := map[string]string{} // yaml name → synthetic id
	for _, m := range c.Models {
		id := modelID(m.Name)
		modelIDs[m.Name] = id
		model := storage.Model{
			ID: id, Name: m.Name, Balance: storage.BalanceWeighted,
			EnableAuth: m.EnableAuth, IsEnabled: true,
		}
		for _, t := range m.Targets {
			pid, ok := providerIDs[t.Provider]
			if !ok {
				return nil, fmt.Errorf("model %q references unknown provider %q", m.Name, t.Provider)
			}
			model.Targets = append(model.Targets, storage.ModelBackend{
				ProviderID: pid, Model: t.Model, Weight: 1,
			})
		}
		b.SetModel(model)
	}

	for _, k := range c.APIKeys {
		if k.Key == "" {
			continue
		}
		id := apiKeyID(k.Name)
		bound := map[string]bool{}
		for _, name := range k.Models {
			mid, ok := modelIDs[name]
			if !ok {
				return nil, fmt.Errorf("api key %q references unknown model %q", k.Name, name)
			}
			bound[mid] = true
		}
		b.SetAPIKey(k.Key, storage.ApiKeyAccessRecord{
			ID: id, Name: k.Name, IsEnabled: true,
		}, bound)
	}

	return b.Done(), nil
}

// providerID derives a stable synthetic provider id from its YAML name.
func providerID(name string) string { return "provider:" + name }

// modelID derives a stable synthetic model id from its YAML name.
func modelID(name string) string { return "model:" + name }

// apiKeyID derives a stable synthetic api-key id from its YAML name.
func apiKeyID(name string) string { return "apikey:" + name }
