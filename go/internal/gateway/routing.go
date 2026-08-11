package gateway

import (
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
)

func routingTargets(route storage.Route) ([]router.Target, router.Strategy) {
	targets := make([]router.Target, 0, len(route.Upstreams))
	for _, target := range route.Upstreams {
		targets = append(targets, router.Target{
			UpstreamID: target.UpstreamID,
			Model:      target.Model,
			Weight:     target.Weight,
			Priority:   target.Priority,
		})
	}
	return targets, router.Strategy(route.Balance)
}
