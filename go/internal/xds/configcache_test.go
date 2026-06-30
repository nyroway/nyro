package xds

import (
	"sort"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newPopulatedStorage builds a memory storage with one provider, two models
// (one open, one auth-gated), and two API keys (one bound to the gated model).
func newPopulatedStorage(t *testing.T) (*memory.Backend, storage.Provider, storage.Model, storage.Model, storage.ApiKey) {
	t.Helper()
	st := memory.New()

	p, err := st.Providers().Create(storage.CreateProvider{
		Name: "openai", Vendor: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com", APIKey: "sk-upstream",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	mOpen, err := st.Models().Create(storage.CreateModel{
		Name: "gpt-open", Targets: []storage.CreateModelBackend{
			{ProviderID: p.ID, Model: "gpt-4o", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create open model: %v", err)
	}

	mGated, err := st.Models().Create(storage.CreateModel{
		Name: "gpt-gated", EnableAuth: true, Targets: []storage.CreateModelBackend{
			{ProviderID: p.ID, Model: "gpt-4o-gated", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create gated model: %v", err)
	}

	rpm := int32(100)
	k, err := st.APIKeys().Create(storage.CreateApiKey{
		Name: "alice", Token: "tok-alice", RPM: &rpm, ModelIDs: []string{mGated.ID},
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	if err := st.Settings().Set("proxy_enabled", "true"); err != nil {
		t.Fatalf("set proxy_enabled: %v", err)
	}
	if err := st.Settings().Set("proxy_url", "http://proxy.local:8080"); err != nil {
		t.Fatalf("set proxy_url: %v", err)
	}
	return st, p, mOpen, mGated, k.ApiKey
}

func TestLoadFromStorage_BuildsAllMaps(t *testing.T) {
	st, p, mOpen, mGated, k := newPopulatedStorage(t)

	snap, err := LoadFromStorage(st.Storage())
	if err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	// providers
	got := snap.ProviderGet(p.ID)
	if got == nil || got.BaseURL != p.BaseURL || !got.IsEnabled {
		t.Errorf("ProviderGet = %v; want populated provider", got)
	}
	if snap.ProviderGet("nope") != nil {
		t.Error("ProviderGet missing key should return nil")
	}

	// models (with targets)
	gm := snap.ModelByName(mOpen.Name)
	if gm == nil || len(gm.Targets) != 1 || gm.Targets[0].ProviderID != p.ID {
		t.Errorf("ModelByName(open) = %+v; want 1 target on provider", gm)
	}
	if snap.ModelByName(mGated.Name).EnableAuth != true {
		t.Error("gated model EnableAuth not carried")
	}
	if snap.ModelByName("missing") != nil {
		t.Error("ModelByName missing should return nil")
	}

	// models list
	names := []string{}
	for _, m := range snap.ModelsList() {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	want := []string{mGated.Name, mOpen.Name}
	sort.Strings(want)
	if len(names) != 2 {
		t.Errorf("ModelsList len = %d; want 2", len(names))
	}

	// apikeys
	rec := snap.FindAPIKey(k.Token)
	if rec == nil || rec.ID != k.ID || rec.Name != "alice" || !rec.IsEnabled || rec.RPM == nil || *rec.RPM != 100 {
		t.Errorf("FindAPIKey = %+v; want alice rpm=100", rec)
	}
	if snap.FindAPIKey("nope") != nil {
		t.Error("FindAPIKey missing token should return nil")
	}

	// bindings: alice bound to gated model only
	if !snap.ModelBindingExists(k.ID, mGated.ID) {
		t.Error("ModelBindingExists(alice, gated) = false; want true")
	}
	if snap.ModelBindingExists(k.ID, mOpen.ID) {
		t.Error("ModelBindingExists(alice, open) = true; want false")
	}
	bound := snap.ListBoundModelIDs(k.ID)
	if len(bound) != 1 || bound[0] != mGated.ID {
		t.Errorf("ListBoundModelIDs = %v; want [%s]", bound, mGated.ID)
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
	st, _, _, _, _ := newPopulatedStorage(t)
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
	if snap == nil || snap.ModelByName("gpt-open") == nil {
		t.Error("Load returned nil/empty snapshot after swap")
	}

	// re-swap with an empty snapshot; readers see it immediately.
	c.Swap(&ConfigSnapshot{
		providers: map[string]storage.Provider{},
		models:    map[string]storage.Model{},
	})
	if c.Load().ModelByName("gpt-open") != nil {
		t.Error("swap did not publish new snapshot")
	}
}

func TestStartLoaderLoop_Refreshes(t *testing.T) {
	st, _, mOpen, _, _ := newPopulatedStorage(t)
	c := &ConfigCache{}
	errCh := make(chan error, 4)

	stop := c.StartLoaderLoop(st.Storage(), 20*time.Millisecond, errCh)
	defer stop()

	// initial snapshot present.
	waitFor(t, time.Second, func() bool { return c.Ready() && c.Load().ModelByName(mOpen.Name) != nil })

	// delete the model; next tick should drop it from the snapshot.
	if err := st.Models().Delete(mOpen.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		s := c.Load()
		return s != nil && s.ModelByName(mOpen.Name) == nil
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
