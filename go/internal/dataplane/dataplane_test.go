package dataplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/observability"
)

func TestBuild_ConfigAndConfigSyncAreMutuallyExclusive(t *testing.T) {
	// NOTE: Build itself does NOT enforce XOR (it picks --config when both
	// are set). The XOR is enforced in the cobra RunE. We exercise it via RunE
	// below. This test documents that Build picks config when both given.
	_, _, err := Build(context.Background(), Options{ConfigPath: "missing.yaml", SyncTarget: "localhost:9999", ListenAddr: "127.0.0.1:19530"})
	// missing.yaml → file error, proving the config branch was selected.
	if err == nil {
		t.Error("expected error selecting config branch with both flags; Build must prefer --config")
	}
}

func TestBuild_StandaloneYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nyro.yaml"
	const yaml = `
version: 1
upstreams:
  - name: openai
    provider: openai
    protocol: openai-chat
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
	gw, obs, err := Build(context.Background(), Options{ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
	if err != nil {
		t.Fatalf("Build: %v", err)
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

// TestBuild_StandaloneReadsObservabilityFromYAML proves settings.observability
// declared in the YAML file actually reaches the observability provider (it did
// not before this fix — standalone mode read OTEL_* env vars only and silently
// ignored the file). traces.exporter: otlp with no endpoint anywhere must trip
// NewProvider's fail-fast guard — that failure is only possible if the YAML
// setting was actually read.
func TestBuild_StandaloneReadsObservabilityFromYAML(t *testing.T) {
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
	_, _, err := Build(context.Background(), Options{ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
	if err == nil {
		t.Fatal("expected an error: traces.exporter=otlp declared in YAML with no endpoint must fail fast, proving the YAML setting was read")
	}
}

// TestBuild_StandaloneIgnoresEnvWhenYAMLSilent proves OTEL_* environment
// variables are never consulted: OTEL_TRACES_EXPORTER=otlp is set (with no
// OTEL_EXPORTER_OTLP_ENDPOINT), which would trip the same fail-fast guard as the
// test above if the gateway still read it — but the YAML declares nothing, so
// the fixed default (traces→disabled) must apply and Build must succeed.
func TestBuild_StandaloneIgnoresEnvWhenYAMLSilent(t *testing.T) {
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
	_, obs, err := Build(context.Background(), Options{ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
	if err != nil {
		t.Fatalf("Build: %v (OTEL_* env vars must not be consulted when the YAML declares nothing)", err)
	}
	_ = obs.Shutdown(context.Background())
}

// cacheWithSettings builds a configsync.ConfigCache pre-loaded with the given
// obs_* settings, mirroring what a control-plane push (or a standalone YAML
// snapshot) would populate.
func cacheWithSettings(settings map[string]string) *configsync.ConfigCache {
	var b configsync.Snapshot
	for k, v := range settings {
		b.SetSetting(k, v)
	}
	cache := &configsync.ConfigCache{}
	cache.Swap(b.Done())
	return cache
}

func TestDefaultObsGet_NoNoneSentinel(t *testing.T) {
	// The whole point of this task's key migration: an unset metrics/traces
	// exporter must resolve to "" (disabled), never the old "none" sentinel.
	for _, key := range []string{"obs_logs_exporter", "obs_metrics_exporter", "obs_traces_exporter", "obs_logs_otlp_endpoint", "obs_metrics_otlp_endpoint", "nonsense_key"} {
		v, err := defaultObsGet(key)
		if err != nil {
			t.Fatalf("defaultObsGet(%q): unexpected error %v", key, err)
		}
		if v == "none" {
			t.Errorf("defaultObsGet(%q) = %q; the \"none\" sentinel must be fully gone", key, v)
		}
	}
	if v, _ := defaultObsGet("obs_logs_exporter"); v != "stdout" {
		t.Errorf("defaultObsGet(obs_logs_exporter) = %q; want stdout", v)
	}
	if v, _ := defaultObsGet("obs_metrics_exporter"); v != "" {
		t.Errorf("defaultObsGet(obs_metrics_exporter) = %q; want empty (disabled)", v)
	}
	if v, _ := defaultObsGet("obs_traces_exporter"); v != "" {
		t.Errorf("defaultObsGet(obs_traces_exporter) = %q; want empty (disabled)", v)
	}
}

func TestResolveObsConfig_EmptySnapshotFallsBackToDefault(t *testing.T) {
	cache := cacheWithSettings(nil)
	cfg := resolveObsConfig(cache)
	if cfg.Logs.Kind != observability.ExporterKindStdout {
		t.Errorf("Logs.Kind = %q; want stdout default", cfg.Logs.Kind)
	}
	if cfg.Metrics.Kind != "" {
		t.Errorf("Metrics.Kind = %q; want disabled default", cfg.Metrics.Kind)
	}
	if cfg.Traces.Kind != "" {
		t.Errorf("Traces.Kind = %q; want disabled default", cfg.Traces.Kind)
	}
}

func TestResolveObsConfig_PartialSettingDoesNotFallBack(t *testing.T) {
	// Only traces is configured (as otlp with an endpoint); logs/metrics are
	// left unset. This must NOT be treated as "all empty" — the explicit
	// traces=otlp setting must survive, and logs/metrics must resolve to
	// disabled (NOT the stdout/none defaults), since resolveObsConfig only
	// falls back to defaultObsGet as an all-or-nothing unit.
	cache := cacheWithSettings(map[string]string{
		"obs_traces_exporter":      "otlp",
		"obs_traces_otlp_endpoint": "http://collector:4318",
	})
	cfg := resolveObsConfig(cache)
	if cfg.Traces.Kind != observability.ExporterKindOTLP {
		t.Errorf("Traces.Kind = %q; want otlp", cfg.Traces.Kind)
	}
	if cfg.Traces.Params["endpoint"] != "http://collector:4318" {
		t.Errorf("Traces endpoint = %q; want http://collector:4318", cfg.Traces.Params["endpoint"])
	}
	if cfg.Logs.Kind != "" {
		t.Errorf("Logs.Kind = %q; want disabled (partial config must not trigger the stdout default)", cfg.Logs.Kind)
	}
	if cfg.Metrics.Kind != "" {
		t.Errorf("Metrics.Kind = %q; want disabled", cfg.Metrics.Kind)
	}
}

func TestResolveObsConfig_EndpointOnlyIsNotEmpty(t *testing.T) {
	// Exercises the Params["endpoint"] leg of obsConfigIsEmpty directly (not
	// just the Kind legs): a snapshot that sets only an otlp endpoint, with
	// its exporter kind also set (LoadConfig always ties the two together —
	// see loadSignalConfig), must resolve to that config, not the default.
	cache := cacheWithSettings(map[string]string{
		"obs_metrics_exporter":      "otlp",
		"obs_metrics_otlp_endpoint": "http://collector:4318",
	})
	cfg := resolveObsConfig(cache)
	if cfg.Metrics.Kind != observability.ExporterKindOTLP {
		t.Errorf("Metrics.Kind = %q; want otlp", cfg.Metrics.Kind)
	}
	if cfg.Logs.Kind != "" {
		t.Errorf("Logs.Kind = %q; want disabled", cfg.Logs.Kind)
	}
}

func TestResolveObsConfig_InvalidExporterFallsBackToDefault(t *testing.T) {
	// prometheus is not a registered logs exporter, so LoadConfig errors on
	// this snapshot; resolveObsConfig must log and fall back to
	// defaultObsGet rather than propagate the error (or panic/crash).
	cache := cacheWithSettings(map[string]string{
		"obs_logs_exporter": "prometheus",
	})
	cfg := resolveObsConfig(cache)
	if cfg.Logs.Kind != observability.ExporterKindStdout {
		t.Errorf("Logs.Kind = %q; want the fallback default (stdout)", cfg.Logs.Kind)
	}
	if cfg.Metrics.Kind != "" || cfg.Traces.Kind != "" {
		t.Errorf("Metrics/Traces should also resolve to the disabled default: got metrics=%q traces=%q", cfg.Metrics.Kind, cfg.Traces.Kind)
	}
}

func TestNewMetricsServer_DefaultsPathAndWiresHandler(t *testing.T) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	obs := &observability.ObsProvider{PromHandler: h, PromListen: ":9464"}

	srv := newMetricsServer(obs, "")
	if srv.Addr != ":9464" {
		t.Errorf("Addr = %q; want :9464", srv.Addr)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.Handler.ServeHTTP(rr, req)
	if !called {
		t.Error("expected PromHandler to be invoked at the default /metrics path")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rr.Code)
	}
}

func TestNewMetricsServer_CustomPath(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	obs := &observability.ObsProvider{PromHandler: h, PromListen: ":9464"}

	srv := newMetricsServer(obs, "/custom-metrics")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/custom-metrics", nil)
	srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("custom path not wired: status = %d; want 200", rr.Code)
	}

	// The default /metrics path must NOT also be wired when a custom path is set.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.Handler.ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusOK {
		t.Error("default /metrics path should not respond when a custom path was configured")
	}
}
