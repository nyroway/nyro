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
