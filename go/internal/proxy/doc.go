// Package proxy implements request forwarding: the single orchestration point
// (dispatch_pipeline), per-protocol ingress shells, protocol negotiation,
// per-request credential resolution, and the two streaming paths — byte-level
// passthrough (when ingress==egress and the vendor declares no mutations) and
// the IR round-trip (decode → AiStreamDelta → hooks → encode).
//
// Ported from crates/nyro-core/src/proxy.
//
// Layer: 3 (serve) — the data-plane orchestrator; may import any lower layer.
// Nothing below layer 3 may import it. This package is deliberately not
// exported outside internal/: it wires together seventeen packages and has no
// reuse value on its own.
package proxy
