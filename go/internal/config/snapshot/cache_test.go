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
	builder.SetRoute(Route{ID: "r1", Model: "gpt-4o"})
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
