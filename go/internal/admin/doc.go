// Package admin implements the management/control plane: API keys (with
// rpm/rpd/tpm/tpd quotas), models and routing backends, providers, OAuth
// sessions, request logs and stats, config import/export, and the loaded
// extensions view. It is the single entry point for all management operations
// and is consumed by the WebUI and, later, the Tauri sidecar.
//
// Ported from crates/nyro-core/src/admin.
package admin
