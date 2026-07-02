package xds

import (
	"sort"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newPopulatedStorage builds a memory backend with one upstream, two routes
// (one open, one auth-gated), and one consumer with one key bound to the
// gated route + a requests quota. Returns the raw key token alongside the
// created entities so tests can exercise FindKey.
func newPopulatedStorage(t *testing.T) (*memory.Backend, storage.Upstream, storage.Route, storage.Route, storage.Consumer, string) {
	t.Helper()
	st := memory.New()
	core := st.Storage()

	u, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "openai", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com", CredentialsJSON: []byte(`{"api_key":"sk-upstream"}`),
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}

	rOpen, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-open", Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: u.ID, Model: "gpt-4o", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create open route: %v", err)
	}

	rGated, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-gated", EnableAuth: true, Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: u.ID, Model: "gpt-4o-gated", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create gated route: %v", err)
	}

	c, err := core.Consumers().Create(storage.CreateConsumer{
		Name:   "alice",
		Keys:   []storage.CreateConsumerKey{{Name: "primary", Token: "nyro_tok_alice_0000"}},
		Routes: []string{rGated.Model},
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
	return st, u, rOpen, rGated, c, c.Keys[0].Token
}

func TestLoadFromStorage_BuildsAllMaps(t *testing.T) {
	st, u, rOpen, rGated, c, rawKey := newPopulatedStorage(t)

	snap, err := LoadFromStorage(st.Storage())
	if err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	// upstreams
	got := snap.UpstreamGet(u.ID)
	if got == nil || got.BaseURL != u.BaseURL || !got.Enabled {
		t.Errorf("UpstreamGet = %v; want populated upstream", got)
	}
	if snap.UpstreamGet("nope") != nil {
		t.Error("UpstreamGet missing key should return nil")
	}

	// routes (with targets)
	gr := snap.RouteByModel(rOpen.Model)
	if gr == nil || len(gr.Upstreams) != 1 || gr.Upstreams[0].UpstreamID != u.ID {
		t.Errorf("RouteByModel(open) = %+v; want 1 target on upstream", gr)
	}
	if !snap.RouteByModel(rGated.Model).EnableAuth {
		t.Error("gated route EnableAuth not carried")
	}
	if snap.RouteByModel("missing") != nil {
		t.Error("RouteByModel missing should return nil")
	}

	// routes list
	names := []string{}
	for _, r := range snap.RoutesList() {
		names = append(names, r.Model)
	}
	sort.Strings(names)
	want := []string{rGated.Model, rOpen.Model}
	sort.Strings(want)
	if len(names) != 2 {
		t.Errorf("RoutesList len = %d; want 2", len(names))
	}

	// consumer key auth
	rec := snap.FindKey(rawKey)
	if rec == nil || rec.ConsumerID != c.ID || !rec.Enabled {
		t.Errorf("FindKey = %+v; want alice's key", rec)
	}
	if snap.FindKey("nope") != nil {
		t.Error("FindKey missing token should return nil")
	}

	// route grants: alice bound to gated route only
	if len(rec.Routes) != 1 || rec.Routes[0] != rGated.Model {
		t.Errorf("rec.Routes = %v; want [%s]", rec.Routes, rGated.Model)
	}

	// quotas
	if len(rec.Quotas) != 1 || rec.Quotas[0].QuotaLimit != 100 {
		t.Errorf("rec.Quotas = %+v; want 1 requests quota limit 100", rec.Quotas)
	}

	// settings
	if v, ok := snap.SettingGet("proxy_enabled"); !ok || v != "true" {
		t.Errorf("SettingGet(proxy_enabled) = %q %v; want true", v, ok)
	}
	if v, ok := snap.SettingGet("proxy_url"); !ok || v == "" {
		t.Errorf("SettingGet(proxy_url) = %q %v; want non-empty", v, ok)
	}
	if _, ok := snap.SettingGet("absent"); ok {
		t.Error("SettingGet absent should be ok=false")
	}
}

func TestConfigCache_LoadSwapReady(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	c := &ConfigCache{}
	if c.Ready() {
		t.Fatal("fresh cache should not be Ready")
	}
	if c.Load() != nil {
		t.Fatal("fresh cache Load should be nil")
	}
	if err := c.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("LoadAndSwap: %v", err)
	}
	if !c.Ready() {
		t.Fatal("cache should be Ready after LoadAndSwap")
	}
	snap := c.Load()
	if snap == nil || snap.RouteByModel("gpt-open") == nil {
		t.Error("Load returned nil/empty snapshot after swap")
	}

	// re-swap with an empty snapshot; readers see it immediately.
	c.Swap((&Snapshot{}).Done())
	if c.Load().RouteByModel("gpt-open") != nil {
		t.Error("swap did not publish new snapshot")
	}
}

func TestStartLoaderLoop_Refreshes(t *testing.T) {
	st, _, rOpen, _, _, _ := newPopulatedStorage(t)
	c := &ConfigCache{}
	errCh := make(chan error, 4)

	stop := c.StartLoaderLoop(st.Storage(), 20*time.Millisecond, errCh)
	defer stop()

	// initial snapshot present.
	waitFor(t, time.Second, func() bool { return c.Ready() && c.Load().RouteByModel(rOpen.Model) != nil })

	// delete the route; next tick should drop it from the snapshot.
	if err := st.Storage().Routes().Delete(rOpen.ID); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s := c.Load()
		return s != nil && s.RouteByModel(rOpen.Model) == nil
	})
}

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", d)
}
