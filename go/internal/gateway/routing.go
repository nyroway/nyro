package gateway

import (
	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/routing"
)

func routingTargets(route configsnapshot.Route) ([]routing.Target, routing.Strategy) {
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

func routeTargetID(route configsnapshot.Route, selected routing.Target) string {
	for _, target := range route.Upstreams {
		if target.UpstreamID == selected.UpstreamID &&
			target.Model == selected.Model &&
			target.Weight == selected.Weight &&
			target.Priority == selected.Priority {
			return target.ID
		}
	}
	return ""
}
