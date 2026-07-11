// Package pki provides the offline certificate authority and TLS config
// construction used to secure the config-sync gRPC channel between admin
// (control plane) and gateway (data plane) nodes.
//
// The trust model is a single self-signed CA per deployment (or an external
// BYO PKI producing files in the same shape): the CA signs a server leaf
// certificate for admin (DNS/IP SANs from --advertise) and a client leaf
// certificate per gateway (a SPIFFE URI SAN carrying the node identity,
// spiffe://nyro/gateway/<node-id>). admin/gateway load these at runtime via
// LoadServerTLS/LoadClientTLS from three explicit file paths — there is no
// directory-scanning or auto-discovery at runtime; only the offline `nyro ca`
// commands write to a conventional directory.
//
// Everything in this package is pure (reads/writes only the paths passed in)
// so it is fully unit-testable without a running server.
package pki
