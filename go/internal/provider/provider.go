// Package provider holds the built-in vendor definitions. Each vendor is a
// self-registering file that owns its protocols, default model, credential
// schema, model discovery, and authentication (NewAuthenticator). The control
// plane consumes Definition via Lookup/Definitions; the data plane consumes
// NewAuthenticator.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Provider is a built-in provider adapter shared by the control plane and the
// data plane. The control plane consumes the static Definition (configuration
// shape, model discovery, credential schema); the data plane consumes
// NewAuthenticator for outbound authentication and request preparation.
type Provider interface {
	// Definition returns the provider's static description (control-plane data).
	Definition() Definition
	// NewAuthenticator returns the outbound authenticator for one upstream.
	NewAuthenticator(ctx context.Context, upstream UpstreamRuntime) (Authenticator, error)
}

// Definition is a provider's static description: configuration shape, model
// discovery, credential schema, and provider-specific extension data. It is
// pure data with no net/http dependency and is consumed directly by the
// control plane (admin/config/preset derivation). Add provider-specific fields
// here — or carry them via Extra — without breaking the Provider interface.
type Definition struct {
	ID              string
	Name            string
	DefaultProtocol string
	DefaultModel    string
	Protocols       []Protocol
	Credentials     CredentialSchema
	Models          ModelDiscovery
	Extra           map[string]any // provider-level custom data (e.g. anthropic_version, api_version)
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
	ModelsJSON      json.RawMessage
	ProxyURL        string
}

// ModelDiscovery describes how a provider's models are discovered.
type ModelDiscovery struct {
	Kind   string   // KindDynamic: fetch from URL; KindStatic: use Values
	URL    string   // KindDynamic: full discovery URL (response parsed as openai {data:[{id}]})
	Values []string // KindStatic: model list
}

// ModelDiscovery kinds.
const (
	KindDynamic = "dynamic"
	KindStatic  = "static"
)

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
	registry   = map[string]Provider{}
)

// Register adds a built-in provider to the registry. A nil provider, an empty
// ID, or a duplicate ID panic — mirroring database/sql.Register — so wiring
// mistakes surface immediately at startup rather than as silent overrides.
func Register(p Provider) {
	if p == nil {
		return
	}
	id := p.Definition().ID
	if id == "" {
		panic("provider: Register called with empty Definition ID")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("provider: duplicate registration: %q", id))
	}
	registry[id] = p
}

// Get returns a registered provider by id (data-plane entry: holds behavior).
func Get(id string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[id]
	return p, ok
}

// Lookup returns a registered provider's static description by id (control-plane
// entry: pure data, no behavior). Named Lookup — not Definition — because Go
// keeps types and functions in one package scope, so a func Definition would
// clash with the Definition type above.
func Lookup(id string) (Definition, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[id]
	if !ok {
		return Definition{}, false
	}
	return p.Definition(), true
}

// List returns all providers in stable ID order (data-plane).
func List() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Provider, 0, len(ids))
	for _, id := range ids {
		out = append(out, registry[id])
	}
	return out
}

// Definitions returns every provider's static description in stable ID order
// (control-plane). This is the single source of truth for vendor presets.
func Definitions() []Definition {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Definition, 0, len(ids))
	for _, id := range ids {
		out = append(out, registry[id].Definition())
	}
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
// It prefers DefaultModel, then the first discovered/static model.
func HealthCheckModel(d Definition) string {
	if d.DefaultModel != "" {
		return d.DefaultModel
	}
	if models := d.Models.Values; len(models) > 0 {
		return models[0]
	}
	return ""
}
