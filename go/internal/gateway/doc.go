// Package gateway is the transitional owner of Snapshot-bound LLM Runtime
// construction and Provider HTTP transports.
//
// Ported from crates/nyro-core/src/proxy.
//
// Layer: 3 (serve) — Task 10 replaces this adapter with Kernel generation
// composition. Nothing below layer 3 may import it.
//
// The gateway root consumes internal/config/snapshot only. Config-sync
// transport is assembled by the internal/gateway/runtime composition root.
package gateway
