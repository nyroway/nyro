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
	core := st.Core()
	if err := cfg.ApplyTo(core); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	// upstream seeded
	ups, _ := core.Upstreams().List()
	if len(ups) != 1 || ups[0].Name != "openai" {
		t.Errorf("upstream not seeded: %+v", ups)
	}
	// route seeded with a target on the upstream
	routes, _ := core.Routes().List()
	if len(routes) != 1 || routes[0].Model != "gpt-4o" {
		t.Errorf("route not seeded: %+v", routes)
	}
	if len(routes[0].Upstreams) != 1 || routes[0].Upstreams[0].UpstreamID != ups[0].ID {
		t.Errorf("route target binding wrong: %+v", routes[0].Upstreams)
	}
	// consumer with explicit token + route grant
	consumers, _ := core.Consumers().List()
	if len(consumers) != 1 || len(consumers[0].Keys) != 1 {
		t.Errorf("consumer not seeded: %+v", consumers)
	}
	if len(consumers[0].Routes) != 1 || consumers[0].Routes[0] != "gpt-4o" {
		t.Errorf("consumer route grant wrong: %+v", consumers[0].Routes)
	}
	rec, _ := core.Auth().FindKey("nyro-secret")
	if rec == nil {
		t.Error("explicit token not discoverable after ApplyTo")
	}
}

func TestApplyToUnknownProvider(t *testing.T) {
	cfg := &Config{
		Models: []ModelSpec{{Name: "m", Targets: []ModelTargetSpec{{Provider: "nope", Model: "x"}}}},
	}
	if err := cfg.ApplyTo(memory.New().Core()); err == nil {
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
	// upstream
	u := snap.UpstreamGet("upstream:openai")
	if u == nil || u.BaseURL != "https://api.openai.com" || string(u.CredentialsJSON) != `{"api_key":"sk-x"}` {
		t.Errorf("upstream missing/wrong: %+v", u)
	}
	// route + target
	rt := snap.RouteByModel("gpt-4o")
	if rt == nil || len(rt.Upstreams) != 1 || rt.Upstreams[0].UpstreamID != "upstream:openai" {
		t.Errorf("route missing/wrong: %+v", rt)
	}
	// consumer key + route grant
	rec := snap.FindKey("nyro-secret")
	if rec == nil {
		t.Fatalf("key missing")
	}
	if len(rec.Routes) != 1 || rec.Routes[0] != "gpt-4o" {
		t.Errorf("route grant missing/wrong: %+v", rec.Routes)
	}
}

func TestBuildSnapshot_UnknownRefs(t *testing.T) {
	// unknown provider in a model target
	cfg := &Config{Models: []ModelSpec{{Name: "m", Targets: []ModelTargetSpec{{Provider: "nope", Model: "x"}}}}}
	if _, err := cfg.BuildSnapshot(); err == nil {
		t.Error("expected error for unknown provider reference")
	}
}
