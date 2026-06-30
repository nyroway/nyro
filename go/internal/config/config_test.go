package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func TestLoadYAMLAndApplyTo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nyro.yaml")
	const yaml = `
providers:
  - name: openai
    protocol: openai-compatible
    base_url: https://api.openai.com
    api_key: sk-***
models:
  - name: gpt-4o
    targets:
      - {provider: openai, model: gpt-4o}
api_keys:
  - name: local
    key: nyro-secret
    models: [gpt-4o]
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openai" {
		t.Errorf("providers parsed wrong: %+v", cfg.Providers)
	}

	st := memory.New()
	if err := cfg.ApplyTo(st); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	// provider seeded
	ps, _ := st.Providers().List()
	if len(ps) != 1 || ps[0].Name != "openai" {
		t.Errorf("provider not seeded: %+v", ps)
	}
	// model seeded with binding to the provider
	ms, _ := st.Models().List()
	if len(ms) != 1 || ms[0].Name != "gpt-4o" {
		t.Errorf("model not seeded: %+v", ms)
	}
	backends, _ := st.ModelBackends().ListByModel(ms[0].ID)
	if len(backends) != 1 || backends[0].ProviderID != ps[0].ID {
		t.Errorf("model backend binding wrong: %+v", backends)
	}
	// api key with explicit token + model binding
	ks, _ := st.APIKeys().List()
	if len(ks) != 1 || ks[0].Token != "nyro-secret" {
		t.Errorf("api key token wrong: %+v", ks)
	}
	rec, _ := st.Auth().FindAPIKey("nyro-secret")
	if rec == nil {
		t.Error("explicit token not discoverable after ApplyTo")
	}
}

func TestApplyToUnknownProvider(t *testing.T) {
	cfg := &Config{
		Models: []ModelSpec{{Name: "m", Targets: []ModelTargetSpec{{Provider: "nope", Model: "x"}}}},
	}
	if err := cfg.ApplyTo(memory.New()); err == nil {
		t.Error("expected error for unknown provider reference")
	}
}

func TestBuildSnapshot_BuildsReadableSnapshot(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderSpec{{
			Name: "openai", Vendor: "openai", Protocol: "openai",
			BaseURL: "https://api.openai.com", APIKey: "sk-x",
		}},
		Models: []ModelSpec{{
			Name: "gpt-4o", Targets: []ModelTargetSpec{{Provider: "openai", Model: "gpt-4o"}},
		}},
		APIKeys: []APIKeySpec{{Name: "local", Key: "nyro-secret", Models: []string{"gpt-4o"}}},
	}
	snap, err := cfg.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	// provider
	p := snap.ProviderGet("provider:openai")
	if p == nil || p.BaseURL != "https://api.openai.com" || p.APIKey != "sk-x" {
		t.Errorf("provider missing/wrong: %+v", p)
	}
	// model + target
	m := snap.ModelByName("gpt-4o")
	if m == nil || len(m.Targets) != 1 || m.Targets[0].ProviderID != "provider:openai" {
		t.Errorf("model missing/wrong: %+v", m)
	}
	// apikey + binding
	rec := snap.FindAPIKey("nyro-secret")
	if rec == nil || rec.Name != "local" {
		t.Errorf("apikey missing/wrong: %+v", rec)
	}
	if !snap.ModelBindingExists("apikey:local", "model:gpt-4o") {
		t.Error("binding missing")
	}
}

func TestBuildSnapshot_UnknownRefs(t *testing.T) {
	// unknown provider in a model target
	cfg := &Config{Models: []ModelSpec{{Name: "m", Targets: []ModelTargetSpec{{Provider: "nope", Model: "x"}}}}}
	if _, err := cfg.BuildSnapshot(); err == nil {
		t.Error("expected error for unknown provider reference")
	}
	// unknown model in an api key
	cfg2 := &Config{
		Models:  []ModelSpec{{Name: "m", Targets: []ModelTargetSpec{}}},
		APIKeys: []APIKeySpec{{Name: "k", Key: "tok", Models: []string{"nope"}}},
	}
	if _, err := cfg2.BuildSnapshot(); err == nil {
		t.Error("expected error for unknown model reference in api key")
	}
}
