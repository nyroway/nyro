// Package plugin implements the request-lifecycle extension framework.
//
// It defines the fixed five phases — OnRequest, OnAccess, OnUpstream,
// OnResponse, OnLog — the PhaseHook interface, the per-request PhaseCtx and
// typed ContextBag.
//
// Ported from crates/nyro-core/src/plugin. Go has no link-time registration
// like Rust's inventory crate; hooks/codecs/vendors register via init() into a
// global registry, preserving the same closed-set phase model.
package plugin
