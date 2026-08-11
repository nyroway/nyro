// Package gateway implements Nyro's HTTP data plane: request orchestration,
// protocol ingress, routing, upstream dispatch, and streaming responses.
//
// Ported from crates/nyro-core/src/proxy.
//
// Layer: 3 (serve) — the data-plane request orchestrator; may import any lower
// layer. Nothing below layer 3 may import it.
//
// The gateway root consumes internal/config/snapshot only. Config-sync
// transport is assembled by the internal/gateway/runtime composition root.
package gateway
