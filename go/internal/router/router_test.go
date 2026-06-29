package router

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func TestSelectPriority(t *testing.T) {
	r := New()
	backends := []storage.ModelBackend{
		{ProviderID: "p1", Model: "a", Priority: 2},
		{ProviderID: "p2", Model: "b", Priority: 1},
	}
	ordered := r.Select(backends, storage.BalancePriority)
	if ordered[0].Model != "b" {
		t.Errorf("priority order: first=%+v", ordered[0])
	}
}

func TestSelectSkipsCooldown(t *testing.T) {
	r := New()
	b1 := storage.ModelBackend{ProviderID: "p1", Model: "a"}
	b2 := storage.ModelBackend{ProviderID: "p2", Model: "b"}
	r.Record(KeyOf(b1), false, 0) // b1 → cooldown

	if r.IsHealthy(KeyOf(b1)) {
		t.Error("b1 should be cooling")
	}
	if !r.IsHealthy(KeyOf(b2)) {
		t.Error("b2 should be healthy")
	}
	// b1 cooling → b2 selected first
	ordered := r.Select([]storage.ModelBackend{b1, b2}, storage.BalancePriority)
	if len(ordered) == 0 || ordered[0].ProviderID != "p2" {
		t.Errorf("expected b2 first (b1 cooling): %+v", ordered)
	}

	// when ALL are cooling, Select falls back to all (never hard-fails)
	r.Record(KeyOf(b2), false, 0)
	all := r.Select([]storage.ModelBackend{b1, b2}, storage.BalancePriority)
	if len(all) != 2 {
		t.Errorf("all-cooling fallback should return both: got %d", len(all))
	}
}

func TestRecordLatencyEMA(t *testing.T) {
	r := New()
	k := KeyOf(storage.ModelBackend{ProviderID: "p", Model: "m"})
	r.Record(k, true, 100)
	r.Record(k, true, 200) // EMA: 100*0.7 + 200*0.3 = 130
	r.mu.RLock()
	lat := r.latency[k]
	r.mu.RUnlock()
	if lat < 129 || lat > 131 {
		t.Errorf("EMA latency=%v, want ~130", lat)
	}
}
