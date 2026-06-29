// Package auth implements the outbound credential layer: acquiring and
// refreshing upstream OAuth tokens. (Inbound API-key auth lives in the proxy
// package; this package owns outbound OAuth.)
//
// Ported from crates/nyro-core/src/auth/. The AuthDriver interface + scheme +
// registry are the seam; the concrete drivers (Claude PKCE, Codex device-code,
// Vertex service-account) live in the drivers subpackage; the admin OAuth
// session endpoints are wired in admin/oauth.go and the background CAS-locked
// refresh loop runs in proxy/oauth_refresh.go.
package auth

import (
	"context"

	"github.com/nyroway/nyro/go/internal/storage"
)

// AuthScheme is the credential-acquisition scheme for a provider.
type AuthScheme string

const (
	SchemeAPIKey          AuthScheme = "apikey"
	SchemeOAuthAuthCode   AuthScheme = "oauth_auth_code_pkce"
	SchemeOAuthDeviceCode AuthScheme = "oauth_device_code"
	SchemeSetupToken      AuthScheme = "setup_token"
	SchemeCustom          AuthScheme = "custom"
)

// SessionStatus is the state of an in-flight OAuth session.
type SessionStatus string

const (
	StatusPending  SessionStatus = "pending"  // awaiting user action / device polling
	StatusComplete SessionStatus = "complete" // token exchanged
	StatusError    SessionStatus = "error"
)

// StartResult is returned when an OAuth flow begins.
type StartResult struct {
	AuthURL    string // authorization-code flow: URL the user visits
	DeviceCode string // device-code flow: the code entered by the user
	UserCode   string // device-code flow: the user-facing code
	Verifier   string // PKCE code_verifier (held for the exchange step)
	State      string // OAuth state — CSRF guard + Anthropic's echo-back requirement
	ExpiresAt  string
}

// SessionStatusResult is the outcome of polling an in-flight session.
type SessionStatusResult struct {
	Status     SessionStatus
	Error      string
	Credential *storage.OAuthCredential // set when Status == StatusComplete (device-code flow)
}

// AuthDriver acquires and refreshes upstream credentials for one scheme.
// Ported from the AuthDriver trait.
type AuthDriver interface {
	// Name reports the canonical driver key (e.g. "claude-code", "codex").
	Name() string
	// Scheme reports the acquisition scheme this driver implements.
	Scheme() AuthScheme
	// Start begins a flow, returning the user-facing URL/code plus session state.
	Start(ctx context.Context, providerID string) (StartResult, error)
	// Exchange completes an authorization-code flow given the callback code,
	// the PKCE verifier, and the state from Start. For non-PKCE drivers,
	// verifier and state are empty.
	Exchange(ctx context.Context, providerID, code, verifier, state string) (storage.OAuthCredential, error)
	// Refresh obtains a fresh access token from a stored refresh token (CAS-locked).
	Refresh(ctx context.Context, cred storage.OAuthCredential) (storage.OAuthCredential, error)
}

// DevicePoller is an optional capability implemented by device-code drivers
// (e.g. Codex). The admin poll endpoint type-asserts to this interface to drive
// in-flight device-code sessions; auth-code and service-account drivers do not.
type DevicePoller interface {
	PollWithDeviceCode(ctx context.Context, deviceCode string) (SessionStatusResult, error)
}

// Registry maps a driver key (e.g. "claude-code", "codex", "vertexai") to its
// driver. Concrete drivers register via init(); the admin OAuth session layer
// resolves a provider's driver key (from vendor metadata) and dispatches here.
type Registry struct {
	drivers map[string]AuthDriver
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{drivers: map[string]AuthDriver{}}
}

// Register adds a driver under a key.
func (r *Registry) Register(key string, d AuthDriver) { r.drivers[key] = d }

// Get returns the driver for a key, normalizing common aliases.
func (r *Registry) Get(key string) (AuthDriver, bool) {
	d, ok := r.drivers[normalizeDriverKey(key)]
	return d, ok
}

// normalizeDriverKey maps common aliases to canonical driver keys.
func normalizeDriverKey(key string) string {
	switch key {
	case "claude-code", "claude_code", "claude-oauth", "claude_oauth", "claude", "anthropic":
		return "claude-code"
	case "openai-oauth", "openai_oauth", "openai", "codex-cli", "codex", "codex_cli":
		return "codex"
	case "vertexai", "vertex_ai", "vertex", "google":
		return "vertexai"
	}
	return key
}

// List returns all registered drivers' keys.
func (r *Registry) List() []string {
	keys := make([]string, 0, len(r.drivers))
	for k := range r.drivers {
		keys = append(keys, k)
	}
	return keys
}
