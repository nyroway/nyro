// Package provider holds the built-in vendor definitions. Each vendor is a
// self-registering file that contributes a pure-data Definition: protocols,
// default model, credential schema, default discovery URL, and outbound auth
// scheme id. The control plane consumes Definition via Lookup/Definitions;
// the data plane's authentication behavior lives in a small auth-scheme
// registry (authenticator.go) keyed by Definition.Auth, not in this file.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Definition is a provider's static description: configuration shape,
// default model-discovery URL, credential schema, outbound auth scheme id,
// and provider-specific extension data. It is pure data with no net/http
// dependency and is consumed directly by the control plane (admin/config/
// preset derivation) and by the data plane's auth-scheme dispatch
// (AuthenticatorFor, via Auth).
type Definition struct {
	ID              string
	Name            string
	DefaultProtocol string
	DefaultModel    string
	Protocols       []Protocol
	Credentials     CredentialSchema
	// ModelsURL is this provider's default model-discovery endpoint (used as
	// the fallback when an upstream doesn't set its own models_url). Empty
	// for providers with no default discovery endpoint.
	ModelsURL string
	// Auth is the outbound authentication scheme id this provider uses:
	// "bearer" (Authorization: Bearer <api_key>), "anthropic" (x-api-key +
	// anthropic-version), or "gemini" (x-goog-api-key). AuthenticatorFor
	// dispatches on this field (looked up via the upstream's stored
	// `provider` id), falling back to a protocol-keyed default when the
	// provider id is unknown/empty (e.g. "custom" upstreams).
	Auth  string
	Extra map[string]any // provider-level custom data (e.g. anthropic_version, api_version)
	// Priority controls display order in the control-plane preset list
	// (lower sorts first). Vendors without an explicit priority default to
	// 0, which sorts before any positive value — set one explicitly for
	// anything that should NOT be first.
	Priority int
}

// Protocol describes one protocol endpoint supported by a provider.
type Protocol struct {
	ID      string
	BaseURL string
}

// UpstreamRuntime is the provider-facing runtime view of an upstream.
type UpstreamRuntime struct {
	Name            string
	Provider        string
	Protocol        string
	BaseURL         string
	CredentialsJSON json.RawMessage
	ProxyURL        string
}

// CredentialSchema describes the credential object expected by a provider.
type CredentialSchema struct {
	Fields []CredentialField
}

// CredentialField describes one field in an upstream credentials object.
type CredentialField struct {
	Name         string
	Type         string
	Required     bool
	Default      string
	Values       []string
	Env          string
	RequiredWhen map[string]any
}

// Authenticator applies provider-specific authentication to outbound requests.
type Authenticator interface {
	Apply(ctx context.Context, req *http.Request) error
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Definition{}
)

// Register adds a built-in provider preset. An empty ID or a duplicate ID
// panics — wiring mistakes surface immediately at startup.
func Register(d Definition) {
	if d.ID == "" {
		panic("provider: Register called with empty Definition ID")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[d.ID]; dup {
		panic(fmt.Sprintf("provider: duplicate registration: %q", d.ID))
	}
	registry[d.ID] = d
}

// Lookup returns a registered provider's static description by id (case-
// insensitive, trimmed). This is the sole read path — the control plane
// (admin/config/preset derivation) and the data plane (auth scheme lookup)
// both use it.
func Lookup(id string) (Definition, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[normalizeID(id)]
	return d, ok
}

// normalizeID trims and lowercases id before registry lookup. Vendor id
// aliases (zhipu, z.ai, grok, ...) are reintroduced here if/when those
// providers are re-added.
func normalizeID(id string) string {
	return strings.TrimSpace(strings.ToLower(id))
}

// Definitions returns every provider's static description in Priority order
// (ties broken by ID for determinism). This is the single source of truth
// for vendor presets, including their display order in the control plane.
func Definitions() []Definition {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Definition, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SupportsProtocol reports whether a definition supports a protocol id.
func SupportsProtocol(d Definition, protocol string) bool {
	for _, proto := range d.Protocols {
		if proto.ID == protocol {
			return true
		}
	}
	return false
}

// HealthCheckModel returns the model used for provider connectivity checks.
func HealthCheckModel(d Definition) string {
	return d.DefaultModel
}
