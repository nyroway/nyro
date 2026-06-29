// Package auth implements both inbound and outbound authentication.
//
// Inbound: validates the caller's Nyro API key, checks model bindings, and
// enforces real-time rate/token quotas (rpm/rpd/tpm/tpd).
//
// Outbound: the AuthDriver scheme acquires and refreshes upstream credentials
// (Claude PKCE, OpenAI/Codex, Vertex service account) with CAS-locked
// concurrent-refresh protection and proactive background refresh.
//
// Ported from crates/nyro-core/src/auth (drivers) and admin/oauth.rs
// (session lifecycle + runtime resolution).
package auth
