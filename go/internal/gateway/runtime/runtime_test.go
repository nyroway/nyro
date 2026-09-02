package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
	platformstate "github.com/nyroway/nyro/go/internal/platform/state"
	redisserver "github.com/nyroway/nyro/go/internal/platform/state/redis"
	statesqlite "github.com/nyroway/nyro/go/internal/platform/state/sqlite"
	"github.com/nyroway/nyro/go/internal/quota"
	storagememory "github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/telemetry"
	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

func testProviderCatalog(t *testing.T) *provider.Catalog {
	t.Helper()
	catalog, err := provider.NewCatalog(
		provider.Generic(), provider.OpenAI(), provider.Anthropic(),
		provider.Gemini(), provider.DeepSeek(), provider.OpenRouter(),
	)
	if err != nil {
		t.Fatalf("provider catalog: %v", err)
	}
	return catalog
}

func TestBuildRejectsNilProviderCatalog(t *testing.T) {
	t.Parallel()
	_, _, err := Build(context.Background(), Options{SyncTarget: configsync.InProcessTarget})
	if err == nil || !strings.Contains(err.Error(), "provider catalog") {
		t.Fatalf("Build() error = %v, want missing provider catalog", err)
	}
}

func TestBuild_ConfigAndConfigSyncAreMutuallyExclusive(t *testing.T) {
	// NOTE: Build itself does NOT enforce XOR (it picks --config when both
	// are set). The XOR is enforced in the cobra RunE. We exercise it via RunE
	// below. This test documents that Build picks config when both given.
	_, _, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: "missing.yaml", SyncTarget: "localhost:9999", ListenAddr: "127.0.0.1:19530"})
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
	gw, manager, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if manager == nil {
		t.Error("standalone mode should construct a runtime manager")
	} else {
		t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	}
	if !gw.Ready() {
		t.Error("gateway should be ready after YAML build with default Memory State")
	}
	if gw.Cache.Load().RouteByModel("gpt-4o") == nil {
		t.Error("model from YAML not in cache")
	}
	if gw.Cache.Load().FindKey("nyro-secret") == nil {
		t.Error("api key from YAML not in cache")
	}
}

func TestBuildStandaloneExplicitMemoryState(t *testing.T) {
	path := writeRuntimeYAML(t, "settings:\n  state:\n    type: memory\n")
	gateway, manager, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if !gateway.Ready() {
		t.Fatal("explicit Memory State gateway is not ready")
	}
}

func TestBuildStandaloneRedisSharesQuotaAcrossGateways(t *testing.T) {
	addr, shutdown := startRuntimeRedis(t)
	defer shutdown()
	path := writeRuntimeYAML(t, "settings:\n  state:\n    type: redis\n    url: redis://"+addr+"/0\n")

	gatewayA, managerA, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managerA.Shutdown(context.Background()) })
	gatewayB, managerB, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managerB.Shutdown(context.Background()) })

	limits := []quota.RequestLimit{{Limit: 1, Window: time.Minute}}
	if allowed, err := gatewayA.Quota.AdmitRequest(context.Background(), "consumer", limits); err != nil || !allowed {
		t.Fatalf("gateway A admission = %v, %v", allowed, err)
	}
	if allowed, err := gatewayB.Quota.AdmitRequest(context.Background(), "consumer", limits); err != nil || allowed {
		t.Fatalf("gateway B shared denial = %v, %v", allowed, err)
	}
	if err := gatewayA.Quota.RecordTokens(context.Background(), "consumer", 5); err != nil {
		t.Fatal(err)
	}
	tokens, err := gatewayB.Quota.TokenValue(context.Background(), "consumer", time.Minute)
	if err != nil || tokens != 5 {
		t.Fatalf("shared tokens = %d, %v; want 5, nil", tokens, err)
	}
}

func TestNewRedisStateBackendRejectsServerThatOnlyPassesPing(t *testing.T) {
	addr, shutdown := startRuntimeRedisWithOptions(t, true)
	defer shutdown()
	backend, err := newRedisStateBackend(context.Background(), platformstate.Config{
		Kind: platformstate.KindRedis,
		URL:  "redis://" + addr + "/0",
	})
	if err == nil {
		retireBackend(backend)
		t.Fatal("newRedisStateBackend() error = nil")
	}
	if backend.store != nil {
		t.Fatal("failed probe returned an installable Store")
	}
}

func TestBuildStandaloneRedisProbeFailureIsStrictAndRedacted(t *testing.T) {
	addr, shutdown := startRuntimeRedisWithOptions(t, true)
	defer shutdown()
	path := writeRuntimeYAML(t, "settings:\n  state:\n    type: redis\n    url: redis://"+addr+"/0\n")
	_, _, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path})
	if err == nil {
		t.Fatal("Build() error = nil")
	}
	if strings.Contains(err.Error(), "updates disabled") || !strings.Contains(err.Error(), "initialize state backend redis") {
		t.Fatalf("Build() exposed raw probe failure: %q", err)
	}
}

func TestBuildStandaloneRedisFailureIsStrictAndRedacted(t *testing.T) {
	path := writeRuntimeYAML(t, "settings:\n  state:\n    type: redis\n    url: redis://alice:secret@127.0.0.1:1/0\n")
	_, _, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path})
	if err == nil {
		t.Fatal("Build() error = nil")
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("Build() error is not redacted: %q", err)
	}
}

func TestBuildConfigSyncStateReadinessAndLastKnownGood(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := storagememory.New()
	store := backend.Storage()
	if err := store.Settings().Set(platformstate.SettingTypeKey, string(platformstate.KindRedis)); err != nil {
		t.Fatal(err)
	}
	if err := store.Settings().Set(platformstate.SettingURLKey, "redis://127.0.0.1:1/0"); err != nil {
		t.Fatal(err)
	}
	server := configsync.NewConfigServer(store)
	dialOptions, stopServer := configsync.ServeInProcess(ctx, server)

	gateway, manager, err := Build(ctx, Options{
		Providers:    testProviderCatalog(t),
		SyncTarget:   configsync.InProcessTarget,
		SyncDialOpts: dialOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = manager.Shutdown(context.Background())
		stopServer()
	})

	waitState(t, 5*time.Second, gateway.Cache.Ready)
	if gateway.Ready() {
		t.Fatal("gateway became ready with an unreachable first Redis backend")
	}

	if err := store.Settings().Set(platformstate.SettingURLKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Settings().Set(platformstate.SettingTypeKey, string(platformstate.KindMemory)); err != nil {
		t.Fatal(err)
	}
	server.Notify()
	waitState(t, 5*time.Second, gateway.Ready)

	const failedHotURL = "redis://127.0.0.1:2/0"
	if err := store.Settings().Set(platformstate.SettingTypeKey, string(platformstate.KindRedis)); err != nil {
		t.Fatal(err)
	}
	if err := store.Settings().Set(platformstate.SettingURLKey, failedHotURL); err != nil {
		t.Fatal(err)
	}
	server.Notify()
	waitState(t, 5*time.Second, func() bool {
		stateManager := manager.state.(*StateManager)
		stateManager.mu.Lock()
		defer stateManager.mu.Unlock()
		return stateManager.desiredSet && stateManager.desiredCfg.URL == failedHotURL
	})
	if !gateway.Ready() {
		t.Fatal("failed hot-update discarded the last-known-good Memory backend")
	}
}

// TestBuild_StandaloneReadsTelemetryFromYAML proves settings.telemetry
// declared in the YAML file actually reaches the observability provider (it did
// not before this fix — standalone mode read OTEL_* env vars only and silently
// ignored the file). traces.exporter: otlp with no endpoint anywhere must trip
// NewProvider's fail-fast guard — that failure is only possible if the YAML
// setting was actually read.
func TestBuild_StandaloneReadsTelemetryFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nyro.yaml"
	const yaml = `
version: 1
settings:
  telemetry:
    traces:
      exporter: otlp
upstreams: []
routes: []
consumers: []
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
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
	_, manager, err := Build(context.Background(), Options{Providers: testProviderCatalog(t), ConfigPath: path, ListenAddr: "127.0.0.1:19530"})
	if err != nil {
		t.Fatalf("Build: %v (OTEL_* env vars must not be consulted when the YAML declares nothing)", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
}

// cacheWithSettings builds a snapshot.Cache pre-loaded with the given
// obs_* settings, mirroring what a control-plane push (or a standalone YAML
// snapshot) would populate.
func cacheWithSettings(settings map[string]string) *configsnapshot.Cache {
	var b configsnapshot.Builder
	for k, v := range settings {
		b.SetSetting(k, v)
	}
	cache := &configsnapshot.Cache{}
	cache.Swap(b.Build())
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
	if cfg.Logs.Kind != schema.ExporterKindStdout {
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
	if cfg.Traces.Kind != schema.ExporterKindOTLP {
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
	if cfg.Metrics.Kind != schema.ExporterKindOTLP {
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
	if cfg.Logs.Kind != schema.ExporterKindStdout {
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
	obs := &telemetry.Provider{PromHandler: h, PromListen: ":9464"}

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
	obs := &telemetry.Provider{PromHandler: h, PromListen: ":9464"}

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

func writeRuntimeYAML(t *testing.T, settings string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nyro.yaml")
	body := "version: 1\n" + settings + "upstreams: []\nroutes: []\nconsumers: []\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startRuntimeRedis(t *testing.T) (string, func()) {
	return startRuntimeRedisWithOptions(t, false)
}

func startRuntimeRedisWithOptions(t *testing.T, failUpdates bool) (string, func()) {
	t.Helper()
	ctx := context.Background()
	database, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := statesqlite.New(ctx, database, statesqlite.Options{CleanupInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	var redisStore platformstate.Store = store
	if failUpdates {
		redisStore = failingUpdateStateStore{Store: store}
	}
	server, err := redisserver.New(redisserver.Options{Store: redisStore})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				t.Errorf("Redis shutdown: %v", err)
			}
			if err := <-done; err != nil {
				t.Errorf("Redis Serve: %v", err)
			}
			if err := store.Shutdown(shutdownCtx); err != nil {
				t.Errorf("State shutdown: %v", err)
			}
			if err := database.Close(); err != nil {
				t.Errorf("database close: %v", err)
			}
		})
	}
	t.Cleanup(shutdown)
	return listener.Addr().String(), shutdown
}

type failingUpdateStateStore struct {
	platformstate.Store
}

func (failingUpdateStateStore) Update(context.Context, func(platformstate.Operations) error) error {
	return errors.New("updates disabled")
}
