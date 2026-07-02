// Package xds is nyro's config-distribution plane: it holds both halves of
// the gateway/admin config-sync loop.
//
//   - The push side: a custom gRPC ConfigService that pushes full config
//     snapshots from the admin (control plane) to gateways (data plane). The
//     name borrows Envoy's push model as a concept only — this is NOT the
//     Envoy xDS protocol (no ADS, no delta/SotW variants, no ACK/NACK).
//   - The read side: the gateway's in-memory configuration cache
//     (ConfigCache), which serves the gateway's config reads — upstreams,
//     routes, consumer keys, and proxy/observability settings — from the last
//     snapshot received (or built directly from YAML in standalone mode).
//
// The admin process runs the gRPC server alongside its REST API; each gateway
// opens a long-lived StreamConfig stream and receives a full snapshot on
// connect and on every config change.
package xds
