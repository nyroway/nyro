package gateway

import (
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/storage"
)

func routingTargets(route storage.Route) ([]routing.Target, routing.Strategy) {
	targets := make([]routing.Target, 0, len(route.Upstreams))
	for _, target := range route.Upstreams {
		targets = append(targets, routing.Target{
			UpstreamID: target.UpstreamID,
			Model:      target.Model,
			Weight:     target.Weight,
			Priority:   target.Priority,
		})
	}
	return targets, routing.Strategy(route.Balance)
}
