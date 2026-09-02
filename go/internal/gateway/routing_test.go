package gateway

import (
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/routing"
)

func TestRoutingTargetsProjectsSelectionFields(t *testing.T) {
	route := configsnapshot.Route{
		Balance: string(routing.StrategyLatency),
		Upstreams: []configsnapshot.RouteTarget{{
			ID:         "binding-1",
			RouteID:    "route-1",
			UpstreamID: "upstream-1",
			Model:      "provider-model",
			Weight:     70,
			Priority:   2,
			Enabled:    false,
		}},
	}

	targets, strategy := routingTargets(route)
	if strategy != routing.StrategyLatency {
		t.Fatalf("strategy = %q, want %q", strategy, routing.StrategyLatency)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	want := routing.Target{UpstreamID: "upstream-1", Model: "provider-model", Weight: 70, Priority: 2}
	if targets[0] != want {
		t.Fatalf("target = %+v, want %+v", targets[0], want)
	}
}

func TestRoutingTargetsPreservesStrategyValue(t *testing.T) {
	for _, value := range []string{
		"",
		string(routing.StrategyWeighted),
		string(routing.StrategyPriority),
		string(routing.StrategyCooldown),
		string(routing.StrategyLatency),
		"future-strategy",
	} {
		t.Run(string(value), func(t *testing.T) {
			_, strategy := routingTargets(configsnapshot.Route{Balance: value})
			if strategy != routing.Strategy(value) {
				t.Fatalf("strategy = %q, want raw value %q", strategy, value)
			}
		})
	}
}
