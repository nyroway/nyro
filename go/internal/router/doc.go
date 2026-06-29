// Package router implements request routing: model-name matching, the
// Model → ModelBackend fan-out, target-selection strategies
// (weighted/priority/latency/cooldown), and the per-target health registry
// that skips unhealthy upstreams and records success/failure.
//
// Ported from crates/nyro-core/src/router.
package router
