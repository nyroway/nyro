// Package config holds bootstrap configuration for the Nyro gateway.
//
// Ported from crates/nyro-core/src/config.rs. Only process-bootstrap values
// live here (listen address, data dir, storage backend selection); the live
// provider/model/key configuration is persisted in storage and surfaced
// through the admin service at runtime.
package config
