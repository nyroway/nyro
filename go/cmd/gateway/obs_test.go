package gateway

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/observability"
)

// buildManager wires an obsManager the way buildGateway's config-server branch
// does: an initial provider resolved from cache, a SwappableProvider, and the
// rebuild callback registered on the cache. Returns the manager and cache so a
// test can push new snapshots and observe the hot-reload.
func buildManager(t *testing.T, cache *configsync.ConfigCache) *obsManager {
	t.Helper()
	obsCfg := resolveObsConfig(cache)
	prov, err := observability.NewProvider(context.Background(), obsCfg)
	if err != nil {
		t.Fatalf("initial NewProvider: %v", err)
	}
	sp := observability.NewSwappableProvider(prov)
	mgr := newObsManager(context.Background(), cache, sp, prov, obsCfg)
	cache.SetOnSwap(mgr.rebuild)
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })
	return mgr
}

// snapshotWith builds a config snapshot carrying the given settings, mirroring
// what a control-plane push populates.
func snapshotWith(settings map[string]string) *configsync.ConfigSnapshot {
	var b configsync.Snapshot
	for k, v := range settings {
		b.SetSetting(k, v)
	}
	return b.Done()
}

// TestObsManager_HotReloadSwitchesLogsToOTLP is the regression test for the
// reported bug: a config-server gateway builds its initial provider from an
// empty cache (→ stdout logs default), and must switch to the otlp exporter the
// control plane pushes with the first snapshot — WITHOUT a restart.
func TestObsManager_HotReloadSwitchesLogsToOTLP(t *testing.T) {
	cache := &configsync.ConfigCache{} // empty: initial resolve → stdout default
	mgr := buildManager(t, cache)

	if got := mgr.lastCfg.Logs.Kind; got != observability.ExporterKindStdout {
		t.Fatalf("initial Logs.Kind = %q; want stdout default", got)
	}
	initial := mgr.current

	// The admin push: all three signals seeded to otlp at the admin's endpoint.
	cache.Swap(snapshotWith(map[string]string{
		"obs_logs_exporter":         "otlp",
		"obs_logs_otlp_endpoint":    "http://127.0.0.1:19531",
		"obs_metrics_exporter":      "otlp",
		"obs_metrics_otlp_endpoint": "http://127.0.0.1:19531",
		"obs_traces_exporter":       "otlp",
		"obs_traces_otlp_endpoint":  "http://127.0.0.1:19531",
	}))

	if got := mgr.lastCfg.Logs.Kind; got != observability.ExporterKindOTLP {
		t.Errorf("after push Logs.Kind = %q; want otlp (hot-reload did not apply)", got)
	}
	if got := mgr.lastCfg.Traces.Kind; got != observability.ExporterKindOTLP {
		t.Errorf("after push Traces.Kind = %q; want otlp", got)
	}
	if mgr.current == initial {
		t.Error("expected the provider to be rebuilt (current still points at the initial provider)")
	}
}

// TestObsManager_RebuildIsNoOpWhenConfigUnchanged ensures a snapshot that does
// not change obs settings (the common case — most pushes are upstream/route
// edits) does not churn the provider.
func TestObsManager_RebuildIsNoOpWhenConfigUnchanged(t *testing.T) {
	cache := &configsync.ConfigCache{}
	mgr := buildManager(t, cache)
	initial := mgr.current

	// A push with no obs settings resolves to the same default → no rebuild.
	cache.Swap(snapshotWith(map[string]string{"proxy.max_retries": "3"}))

	if mgr.current != initial {
		t.Error("provider was rebuilt for a snapshot that did not change obs config")
	}
}

// TestObsManager_PrometheusServerLifecycle proves the scrape server is started
// when the pushed config selects the prometheus metrics exporter, and stopped on
// manager Shutdown. It binds a real, OS-assigned free port.
func TestObsManager_PrometheusServerLifecycle(t *testing.T) {
	addr := freePort(t)

	cache := &configsync.ConfigCache{}
	mgr := buildManager(t, cache) // starts with stdout logs, no metrics server

	if mgr.metricsSrv != nil {
		t.Fatal("no scrape server should run before prometheus is configured")
	}

	cache.Swap(snapshotWith(map[string]string{
		"obs_metrics_exporter":          "prometheus",
		"obs_metrics_prometheus_listen": addr,
	}))

	if mgr.metricsSrv == nil {
		t.Fatal("scrape server should be running after prometheus was configured")
	}
	if !reachable(t, "http://"+addr+"/metrics") {
		t.Fatalf("scrape endpoint never became reachable at %s", addr)
	}

	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("manager Shutdown: %v", err)
	}
	if stillUp(t, "http://"+addr+"/metrics") {
		t.Error("scrape server still accepting connections after Shutdown")
	}
}

// freePort returns an OS-assigned free "127.0.0.1:port" address (closing the
// probe listener immediately — standard TOCTOU tradeoff for this kind of test).
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	return addr
}

func reachable(t *testing.T, url string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func stillUp(t *testing.T, url string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
