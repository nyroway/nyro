// Package proxy implements request forwarding: the single orchestration point
// (dispatch_pipeline), per-protocol ingress shells, protocol negotiation,
// per-request credential resolution, and the two streaming paths — byte-level
// passthrough (when ingress==egress and the vendor declares no mutations) and
// the IR round-trip (decode → AiStreamDelta → hooks → encode).
//
// Ported from crates/nyro-core/src/proxy.
package proxy
