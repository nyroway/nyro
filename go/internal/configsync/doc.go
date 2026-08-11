// Package configsync is nyro's config-distribution plane: it holds both
// halves of the gateway/admin config-sync loop.
//
//   - The push side: a custom gRPC ConfigService that pushes full config
//     snapshots from the admin (control plane) to gateways (data plane) over
//     a single long-lived server-streaming RPC. It is a purpose-built config
//     push mechanism, not an implementation of Envoy's xDS protocol (no ADS,
//     no delta/SotW variants, no ACK/NACK).
//   - The read side: the gateway's in-memory configuration cache
//     (ConfigCache), which serves the gateway's config reads — upstreams,
//     routes, consumer keys, and proxy/observability settings — from the last
//     snapshot received (or built directly from YAML in standalone mode).
//
// The admin process runs the gRPC server alongside its REST API; each gateway
// opens a long-lived StreamConfig stream (advertising its node identity) and
// receives a full snapshot on connect and on every config change. The admin
// tracks connected gateways in memory (see ConfigServer.Nodes) for
// operational visibility — this registry is not persisted.
//
// Layer: 1 (data) — may import internal/protocol/llm, layer 0, and storage.
//
// It imports telemetry/schema (layer 0) to identify exporter settings that
// belong in data-plane snapshots. It does not import the telemetry runtime.
//
// Must not import layer 3 (proxy, router, admin, dataplane, bootstrap).
package configsync
