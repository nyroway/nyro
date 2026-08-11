package gateway

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
)

func TestRoutingTargetsProjectsSelectionFields(t *testing.T) {
	route := storage.Route{
		Balance: storage.BalanceLatency,
		Upstreams: []storage.RouteUpstream{{
			ID:         "binding-1",
			RouteID:    "route-1",
			UpstreamID: "upstream-1",
			Model:      "provider-model",
			Weight:     70,
			Priority:   2,
			Enabled:    false,
			CreatedAt:  "2026-08-11T00:00:00Z",
		}},
	}

	targets, strategy := routingTargets(route)
	if strategy != router.StrategyLatency {
		t.Fatalf("strategy = %q, want %q", strategy, router.StrategyLatency)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	want := router.Target{UpstreamID: "upstream-1", Model: "provider-model", Weight: 70, Priority: 2}
	if targets[0] != want {
		t.Fatalf("target = %+v, want %+v", targets[0], want)
	}
}

func TestRoutingTargetsPreservesStrategyValue(t *testing.T) {
	for _, value := range []storage.ModelBalance{
		"",
		storage.BalanceWeighted,
		storage.BalancePriority,
		storage.BalanceCooldown,
		storage.BalanceLatency,
		"future-strategy",
	} {
		t.Run(string(value), func(t *testing.T) {
			_, strategy := routingTargets(storage.Route{Balance: value})
			if strategy != router.Strategy(value) {
				t.Fatalf("strategy = %q, want raw value %q", strategy, value)
			}
		})
	}
}
