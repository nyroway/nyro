// Package router selects an upstream target among a model's backends and
// tracks per-backend health, cooldown, and latency for failover.
//
// Ported from crates/nyro-core/src/router (matcher/selector/health). Model→
// backends resolution lives in storage; this package owns the SELECTION of
// one backend among many (weighted / priority / cooldown / latency) plus the
// HealthRegistry that drives failover.
package router
