// Package configsync is nyro's config-distribution transport: it connects the
// gateway and admin halves of the config-sync loop.
//
//   - The push side: a custom gRPC ConfigService that pushes full config
//     snapshots from the admin (control plane) to gateways (data plane) over
//     a single long-lived server-streaming RPC. It is a purpose-built config
//     push mechanism, not an implementation of Envoy's xDS protocol (no ADS,
//     no delta/SotW variants, no ACK/NACK).
//   - The receive side: a client that authenticates, decodes full snapshots,
//     and publishes them into config/snapshot.Cache for gateway readers.
//
// The admin process runs the gRPC server alongside its REST API; each gateway
// opens a long-lived StreamConfig stream (advertising its node identity) and
// receives a full snapshot on connect and on every config change. The admin
// tracks connected gateways in memory (see ConfigServer.Nodes) for
// operational visibility — this registry is not persisted.
//
// Snapshot state and storage-backed loading belong to internal/config/snapshot;
// this package owns only gRPC/protobuf transport, authentication, conversion,
// PKI helpers, and connected-node tracking.
//
// Layer: 1 (data) — may import internal/config/snapshot,
// internal/llm/protocol, layer 0, and storage.
//
// It imports platform/state and telemetry/schema (layer 0) to identify State
// and exporter settings that belong in data-plane snapshots. It does not
// import either runtime.
//
// Must not import layer 3 (proxy, router, admin, dataplane, bootstrap).
package configsync
