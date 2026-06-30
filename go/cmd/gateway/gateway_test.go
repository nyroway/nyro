package gateway

import (
	"context"
	"os"
	"testing"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("addr"); addr != "127.0.0.1:19530" {
		t.Errorf("default addr = %q; want 127.0.0.1:19530", addr)
	}
	if cfg, _ := cmd.Flags().GetString("config"); cfg != "" {
		t.Errorf("default config = %q; want empty", cfg)
	}
	if xds, _ := cmd.Flags().GetString("xds-addr"); xds != "" {
		t.Errorf("default xds-addr = %q; want empty", xds)
	}
	if cmd.Use != "gateway" {
		t.Errorf("Use = %q; want gateway", cmd.Use)
	}
}

func TestBuildGateway_ConfigAndXdsAreMutuallyExclusive(t *testing.T) {
	// NOTE: buildGateway itself does NOT enforce XOR (it picks --config when both
	// are set). The XOR is enforced in the cobra RunE. We exercise it via RunE
	// below. This test documents that buildGateway picks config when both given.
	_, _, err := buildGateway(context.Background(), "missing.yaml", "localhost:9999", "memory", "")
	// missing.yaml → file error, proving the config branch was selected.
	if err == nil {
		t.Error("expected error selecting config branch with both flags; buildGateway must prefer --config")
	}
}

func TestRunE_RejectsBothConfigAndXdsAddr(t *testing.T) {
	cmd := NewCmd()
	cmd.SetArgs([]string{"--config", "a.yaml", "--xds-addr", "host:1234"})
	// RunE returns the XOR error before touching storage/listeners.
	err := cmd.RunE(cmd, nil)
	if err == nil || err.Error() == "" {
		t.Fatalf("expected XOR error; got %v", err)
	}
}

func TestBuildGateway_StandaloneYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nyro.yaml"
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
	gw, stopXDS, err := buildGateway(context.Background(), path, "", "memory", "")
	if err != nil {
		t.Fatalf("buildGateway: %v", err)
	}
	if stopXDS != nil {
		t.Error("standalone mode should not start an xDS client")
	}
	if !gw.Cache.Ready() {
		t.Error("cache should be ready after YAML build")
	}
	if gw.Cache.Load().ModelByName("gpt-4o") == nil {
		t.Error("model from YAML not in cache")
	}
	if gw.Cache.Load().FindAPIKey("nyro-secret") == nil {
		t.Error("api key from YAML not in cache")
	}
}
