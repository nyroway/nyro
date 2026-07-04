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
    protocol: openai-chatcompletions
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

// TestBuildGateway_StandaloneReadsObservabilityFromYAML proves settings.observability
// declared in the YAML file actually reaches the observability provider (it did
// not before this fix — standalone mode read OTEL_* env vars only and silently
// ignored the file). traces.exporter: otlp with no endpoint anywhere must trip
// NewProvider's fail-fast guard — that failure is only possible if the YAML
// setting was actually read.
func TestBuildGateway_StandaloneReadsObservabilityFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nyro.yaml"
	const yaml = `
version: 1
settings:
  observability:
    traces:
      exporter: otlp
upstreams: []
routes: []
consumers: []
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := buildGateway(context.Background(), path, "")
	if err == nil {
		t.Fatal("expected an error: traces.exporter=otlp declared in YAML with no endpoint must fail fast, proving the YAML setting was read")
	}
}

// TestBuildGateway_StandaloneIgnoresEnvWhenYAMLSilent proves OTEL_* environment
// variables are never consulted: OTEL_TRACES_EXPORTER=otlp is set (with no
// OTEL_EXPORTER_OTLP_ENDPOINT), which would trip the same fail-fast guard as the
// test above if the gateway still read it — but the YAML declares nothing, so
// the fixed default (traces→none) must apply and buildGateway must succeed.
func TestBuildGateway_StandaloneIgnoresEnvWhenYAMLSilent(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")

	dir := t.TempDir()
	path := dir + "/nyro.yaml"
	const yaml = `
version: 1
upstreams: []
routes: []
consumers: []
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, obs, err := buildGateway(context.Background(), path, "")
	if err != nil {
		t.Fatalf("buildGateway: %v (OTEL_* env vars must not be consulted when the YAML declares nothing)", err)
	}
	_ = obs.Shutdown(context.Background())
}
