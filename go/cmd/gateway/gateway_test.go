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
	_, _, _, err := buildGateway(context.Background(), "missing.yaml", "localhost:9999")
	// missing.yaml → file error, proving the config branch was selected.
	if err == nil {
		t.Error("expected error selecting config branch with both flags; buildGateway must prefer --config")
	}
}

func TestRunE_RejectsBothConfigAndXdsAddr(t *testing.T) {
	cmd := NewCmd()
	// ParseFlags binds the args to the flag set first. Calling RunE directly
	// skips cobra's parse step, so without ParseFlags the flags read as
	// default-empty, the --config/--xds-addr XOR guard would NOT fire, and RunE
	// would fall through to the default branch and block on RunServer (test
	// hang). The XOR check still returns before touching storage/listeners.
	if err := cmd.ParseFlags([]string{"--config", "a.yaml", "--xds-addr", "host:1234"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || err.Error() == "" {
		t.Fatalf("expected XOR error; got %v", err)
	}
}

func TestBuildGateway_StandaloneYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nyro.yaml"
	const yaml = `
version: 1
upstreams:
  - name: openai
    provider: openai
    protocol: openai-compatible
    base_url: https://api.openai.com
    credentials:
      api_key: sk-***
routes:
  - model: gpt-4o
    upstreams:
      - {name: openai, model: gpt-4o}
consumers:
  - name: local
    keys:
      - {name: primary, api_key: nyro-secret}
    routes: [gpt-4o]
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	gw, stopXDS, obs, err := buildGateway(context.Background(), path, "")
	if err != nil {
		t.Fatalf("buildGateway: %v", err)
	}
	if stopXDS != nil {
		t.Error("standalone mode should not start an xDS client")
	}
	if obs == nil {
		t.Error("standalone mode should construct an observability provider")
	} else {
		// Drain the stdout log exporter (default sink) so Shutdown is exercised
		// and the test leaves no dangling periodic-reader goroutine.
		_ = obs.Shutdown(context.Background())
	}
	if !gw.Cache.Ready() {
		t.Error("cache should be ready after YAML build")
	}
	if gw.Cache.Load().RouteByModel("gpt-4o") == nil {
		t.Error("model from YAML not in cache")
	}
	if gw.Cache.Load().FindKey("nyro-secret") == nil {
		t.Error("api key from YAML not in cache")
	}
}
