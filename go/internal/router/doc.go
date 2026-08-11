// Package router selects an upstream target among a model's backends and
// tracks per-backend health, cooldown, and latency for failover.
//
// Ported from crates/nyro-core/src/router (matcher/selector/health). Gateway
// projects route bindings into Target values; this package owns the SELECTION
// of one target among many (weighted / priority / cooldown / latency) plus the
// HealthRegistry that drives failover.
//
// Layer: 0 (foundation) — standard library only. Gateway projects route
// bindings into Target values before calling this package.
package router
