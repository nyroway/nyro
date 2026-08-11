package snapshot

import (
	"sort"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newPopulatedStorage builds a memory backend with one upstream, two routes
// (one open, one auth-gated), and one consumer with one key bound to the
// gated route plus a requests quota.
func newPopulatedStorage(t *testing.T) (*memory.Backend, storage.Upstream, storage.Route, storage.Route, storage.Consumer, string) {
	t.Helper()
	st := memory.New()
	core := st.Storage()

	upstream, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com", CredentialsJSON: []byte(`{"api_key":"sk-upstream"}`),
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}

	openRoute, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-open", Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: upstream.ID, Model: "gpt-4o", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create open route: %v", err)
	}

	gatedRoute, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-gated", EnableAuth: true, Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: upstream.ID, Model: "gpt-4o-gated", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create gated route: %v", err)
	}

	consumer, err := core.Consumers().Create(storage.CreateConsumer{
		Name:   "alice",
		Keys:   []storage.CreateConsumerKey{{Name: "primary", Token: "nyro_tok_alice_0000"}},
		Routes: []string{gatedRoute.Model},
		Quotas: []storage.CreateConsumerQuota{{QuotaType: "requests", QuotaLimit: 100, Window: "1m"}},
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	if err := core.Settings().Set("proxy_enabled", "true"); err != nil {
		t.Fatalf("set proxy_enabled: %v", err)
	}
	if err := core.Settings().Set("proxy_url", "http://proxy.local:8080"); err != nil {
		t.Fatalf("set proxy_url: %v", err)
	}
	return st, upstream, openRoute, gatedRoute, consumer, consumer.Keys[0].Token
}

func TestLoadFromStorageBuildsAllMaps(t *testing.T) {
	st, upstream, openRoute, gatedRoute, consumer, rawKey := newPopulatedStorage(t)

	snap, err := LoadFromStorage(st.Storage())
	if err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	got := snap.UpstreamGet(upstream.ID)
	if got == nil || got.BaseURL != upstream.BaseURL || !got.Enabled {
		t.Errorf("UpstreamGet = %v; want populated upstream", got)
	}
	if snap.UpstreamGet("nope") != nil {
		t.Error("UpstreamGet missing key should return nil")
	}

	route := snap.RouteByModel(openRoute.Model)
	if route == nil || len(route.Upstreams) != 1 || route.Upstreams[0].UpstreamID != upstream.ID {
		t.Errorf("RouteByModel(open) = %+v; want 1 target on upstream", route)
	}
	if !snap.RouteByModel(gatedRoute.Model).EnableAuth {
		t.Error("gated route EnableAuth not carried")
	}
	if snap.RouteByModel("missing") != nil {
		t.Error("RouteByModel missing should return nil")
	}

	names := []string{}
	for _, listedRoute := range snap.RoutesList() {
		names = append(names, listedRoute.Model)
	}
	sort.Strings(names)
	want := []string{gatedRoute.Model, openRoute.Model}
	sort.Strings(want)
	if len(names) != 2 {
		t.Errorf("RoutesList len = %d; want 2", len(names))
	}

	record := snap.FindKey(rawKey)
	if record == nil || record.ConsumerID != consumer.ID || !record.Enabled {
		t.Errorf("FindKey = %+v; want alice's key", record)
	}
	if snap.FindKey("nope") != nil {
		t.Error("FindKey missing token should return nil")
	}
	if len(record.Routes) != 1 || record.Routes[0] != gatedRoute.Model {
		t.Errorf("record.Routes = %v; want [%s]", record.Routes, gatedRoute.Model)
	}
	if len(record.Quotas) != 1 || record.Quotas[0].QuotaLimit != 100 {
		t.Errorf("record.Quotas = %+v; want 1 requests quota limit 100", record.Quotas)
	}

	if value, ok := snap.SettingGet("proxy_enabled"); !ok || value != "true" {
		t.Errorf("SettingGet(proxy_enabled) = %q %v; want true", value, ok)
	}
	if value, ok := snap.SettingGet("proxy_url"); !ok || value == "" {
		t.Errorf("SettingGet(proxy_url) = %q %v; want non-empty", value, ok)
	}
	if _, ok := snap.SettingGet("absent"); ok {
		t.Error("SettingGet absent should be ok=false")
	}
}

func TestCacheLoadSwapReady(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	cache := &Cache{}
	if cache.Ready() {
		t.Fatal("fresh cache should not be Ready")
	}
	if cache.Load() != nil {
		t.Fatal("fresh cache Load should be nil")
	}
	if err := cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("LoadAndSwap: %v", err)
	}
	if !cache.Ready() {
		t.Fatal("cache should be Ready after LoadAndSwap")
	}
	snap := cache.Load()
	if snap == nil || snap.RouteByModel("gpt-open") == nil {
		t.Error("Load returned nil/empty snapshot after swap")
	}

	cache.Swap((&Builder{}).Build())
	if cache.Load().RouteByModel("gpt-open") != nil {
		t.Error("swap did not publish new snapshot")
	}
}

func TestStartLoaderLoopRefreshes(t *testing.T) {
	st, _, openRoute, _, _, _ := newPopulatedStorage(t)
	cache := &Cache{}
	errCh := make(chan error, 4)

	stop := cache.StartLoaderLoop(st.Storage(), 20*time.Millisecond, errCh)
	defer stop()

	waitFor(t, time.Second, func() bool {
		return cache.Ready() && cache.Load().RouteByModel(openRoute.Model) != nil
	})

	if err := st.Storage().Routes().Delete(openRoute.ID); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		snap := cache.Load()
		return snap != nil && snap.RouteByModel(openRoute.Model) == nil
	})
}

func waitFor(t *testing.T, duration time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", duration)
}

func TestCacheOnSwapFiresAfterPublish(t *testing.T) {
	var cache Cache

	var calls int
	var seenModel string
	cache.SetOnSwap(func() {
		calls++
		if snap := cache.Load(); snap != nil {
			if route := snap.RouteByModel("gpt-4o"); route != nil {
				seenModel = route.Model
			}
		}
	})

	var builder Builder
	builder.SetRoute(storage.Route{ID: "r1", Model: "gpt-4o"})
	cache.Swap(builder.Build())

	if calls != 1 {
		t.Errorf("onSwap fired %d times; want 1", calls)
	}
	if seenModel != "gpt-4o" {
		t.Errorf("callback saw model %q; want gpt-4o", seenModel)
	}

	cache.Swap(builder.Build())
	if calls != 2 {
		t.Errorf("onSwap fired %d times after two swaps; want 2", calls)
	}

	cache.SetOnSwap(nil)
	cache.Swap(builder.Build())
	if calls != 2 {
		t.Errorf("onSwap fired %d times after clear; want 2", calls)
	}
}
