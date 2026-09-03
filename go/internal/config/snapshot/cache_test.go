package snapshot

import "testing"

func TestCacheLoadSwapReady(t *testing.T) {
	cache := &Cache{}
	if cache.Ready() {
		t.Fatal("fresh cache should not be Ready")
	}
	if cache.Load() != nil {
		t.Fatal("fresh cache Load should be nil")
	}

	var builder Builder
	builder.SetRoute(Route{ID: "route-1", Model: "gpt-open"})
	cache.Swap(builder.Build())
	if !cache.Ready() {
		t.Fatal("cache should be Ready after Swap")
	}
	if snap := cache.Load(); snap == nil || snap.RouteByModel("gpt-open") == nil {
		t.Error("Load returned nil/empty snapshot after swap")
	}

	cache.Swap((&Builder{}).Build())
	if cache.Load().RouteByModel("gpt-open") != nil {
		t.Error("swap did not publish new snapshot")
	}
}
