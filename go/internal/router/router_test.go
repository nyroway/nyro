package router

import (
	"reflect"
	"testing"
	"time"
)

func TestSelectPriority(t *testing.T) {
	r := New()
	backends := []Target{
		{UpstreamID: "p1", Model: "a", Priority: 2},
		{UpstreamID: "p2", Model: "b", Priority: 1},
	}
	ordered := r.Select(backends, StrategyPriority)
	if ordered[0].Model != "b" {
		t.Errorf("priority order: first=%+v", ordered[0])
	}
}

func TestSelectSkipsCooldown(t *testing.T) {
	r := New()
	b1 := Target{UpstreamID: "p1", Model: "a"}
	b2 := Target{UpstreamID: "p2", Model: "b"}
	r.Record(KeyOf(b1), false, 0) // b1 → cooldown

	if r.IsHealthy(KeyOf(b1)) {
		t.Error("b1 should be cooling")
	}
	if !r.IsHealthy(KeyOf(b2)) {
		t.Error("b2 should be healthy")
	}
	// b1 cooling → b2 selected first
	ordered := r.Select([]Target{b1, b2}, StrategyPriority)
	if len(ordered) == 0 || ordered[0].UpstreamID != "p2" {
		t.Errorf("expected b2 first (b1 cooling): %+v", ordered)
	}

	// when ALL are cooling, Select falls back to all (never hard-fails)
	r.Record(KeyOf(b2), false, 0)
	all := r.Select([]Target{b1, b2}, StrategyPriority)
	if len(all) != 2 {
		t.Errorf("all-cooling fallback should return both: got %d", len(all))
	}
}

func TestRecordLatencyEMA(t *testing.T) {
	r := New()
	k := KeyOf(Target{UpstreamID: "p", Model: "m"})
	r.Record(k, true, 100)
	r.Record(k, true, 200) // EMA: 100*0.7 + 200*0.3 = 130
	r.mu.RLock()
	lat := r.latency[k]
	r.mu.RUnlock()
	if lat < 129 || lat > 131 {
		t.Errorf("EMA latency=%v, want ~130", lat)
	}
}

func TestKeyOfUsesUpstreamAndModel(t *testing.T) {
	if got := KeyOf(Target{UpstreamID: "up", Model: "model"}); got != Key("up:model") {
		t.Fatalf("KeyOf = %q, want up:model", got)
	}
}

func TestSelectEmptyAndSingleTarget(t *testing.T) {
	r := New()
	if got := r.Select(nil, StrategyPriority); len(got) != 0 {
		t.Fatalf("empty Select = %+v, want empty", got)
	}
	one := []Target{{UpstreamID: "only", Model: "m"}}
	got := r.Select(one, StrategyPriority)
	if !reflect.DeepEqual(got, one) {
		t.Fatalf("single Select = %+v, want %+v", got, one)
	}
}

func TestSelectLatencyOrdersLowestEMAFirst(t *testing.T) {
	r := New()
	slow := Target{UpstreamID: "slow", Model: "m"}
	fast := Target{UpstreamID: "fast", Model: "m"}
	r.Record(KeyOf(slow), true, 200)
	r.Record(KeyOf(fast), true, 20)
	ordered := r.Select([]Target{slow, fast}, StrategyLatency)
	if ordered[0] != fast {
		t.Fatalf("first = %+v, want fast target", ordered[0])
	}
}

func TestSelectWeightedReturnsPermutationWithoutMutatingInput(t *testing.T) {
	targets := []Target{
		{UpstreamID: "a", Model: "m", Weight: 0},
		{UpstreamID: "b", Model: "m", Weight: -1},
		{UpstreamID: "c", Model: "m", Weight: 10},
	}
	original := append([]Target(nil), targets...)
	ordered := New().Select(targets, StrategyWeighted)
	if !reflect.DeepEqual(targets, original) {
		t.Fatalf("Select mutated input: got %+v want %+v", targets, original)
	}
	if len(ordered) != len(targets) {
		t.Fatalf("ordered length = %d, want %d", len(ordered), len(targets))
	}
	seen := map[string]bool{}
	for _, target := range ordered {
		if seen[target.UpstreamID] {
			t.Fatalf("duplicate target %q", target.UpstreamID)
		}
		seen[target.UpstreamID] = true
	}
	for _, id := range []string{"a", "b", "c"} {
		if !seen[id] {
			t.Fatalf("missing target %q", id)
		}
	}
}

func TestSelectUnknownStrategyPreservesOrder(t *testing.T) {
	targets := []Target{{UpstreamID: "a"}, {UpstreamID: "b"}}
	ordered := New().Select(targets, Strategy("future-strategy"))
	if !reflect.DeepEqual(ordered, targets) {
		t.Fatalf("ordered = %+v, want %+v", ordered, targets)
	}
}

func TestRecordSuccessClearsCooldown(t *testing.T) {
	r := New()
	target := Target{UpstreamID: "up", Model: "m"}
	key := KeyOf(target)
	r.Record(key, false, 0)
	if r.IsHealthy(key) {
		t.Fatal("target should be cooling after failure")
	}
	r.Record(key, true, 0)
	if !r.IsHealthy(key) {
		t.Fatal("successful record should clear cooldown")
	}
}

func TestRecordBackoffCapsAt64Seconds(t *testing.T) {
	r := New()
	key := KeyOf(Target{UpstreamID: "up", Model: "m"})
	for range 10 {
		r.Record(key, false, 0)
	}
	r.mu.RLock()
	backoff := r.health[key].cooldownUntil.Sub(r.lastUsed[key])
	r.mu.RUnlock()
	if backoff != 64*time.Second {
		t.Fatalf("backoff = %v, want 64s", backoff)
	}
}
