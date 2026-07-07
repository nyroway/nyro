package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// DefaultAnthropicVersion is the anthropic-version header value applied by
// the anthropic auth scheme. It mirrors defaultAnthropicVersion
// (anthropic.go) as a public alias.
const DefaultAnthropicVersion = defaultAnthropicVersion

// anthropicAuthenticator applies the x-api-key + anthropic-version headers
// used by the "anthropic" auth scheme (not Bearer).
type anthropicAuthenticator struct {
	apiKey  string
	version string
}

func (a anthropicAuthenticator) Apply(_ context.Context, req *http.Request) error {
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", a.version)
	return nil
}

// geminiAuthenticator applies either a fixed x-goog-api-key header (Google AI
// Studio native protocol) or an Authorization: Bearer header (OpenAI-
// compatible endpoint), depending on openaiCompatible.
type geminiAuthenticator struct {
	apiKey           string
	openaiCompatible bool
}

func (a geminiAuthenticator) Apply(_ context.Context, req *http.Request) error {
	if a.openaiCompatible {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	} else {
		req.Header.Set("x-goog-api-key", a.apiKey)
	}
	return nil
}

// AuthenticatorFor returns the outbound Authenticator for one upstream.
// providerID is the upstream's stored `provider` (a preset id or "custom");
// it is looked up for its Auth scheme. protocol is used only as a fallback
// when providerID is unknown/unregistered (e.g. "custom", or any legacy row
// predating the provider/auth-scheme split) or the matched Definition has no
// Auth set.
func AuthenticatorFor(providerID, protocol string, up UpstreamRuntime) (Authenticator, error) {
	switch authSchemeFor(providerID, protocol) {
	case "bearer":
		return NewBearerAuthenticator(up.CredentialsJSON)
	case "anthropic":
		return newAnthropicAuthenticator(up.CredentialsJSON)
	case "gemini":
		return newGeminiContentAuthenticator(up.CredentialsJSON)
	default:
		if len(up.CredentialsJSON) == 0 {
			return NoopAuthenticator{}, nil
		}
		return NewBearerAuthenticator(up.CredentialsJSON)
	}
}

// authSchemeFor resolves the auth scheme to dispatch on: the registered
// provider's Definition.Auth when providerID matches one, else a
// protocol-keyed fallback (covers "custom" upstreams, which have no preset,
// and any row whose provider id doesn't match a known preset).
func authSchemeFor(providerID, protocol string) string {
	if def, ok := Lookup(providerID); ok && def.Auth != "" {
		return def.Auth
	}
	if parsed, err := ids.ParseProtocol(protocol); err == nil {
		protocol = parsed.String()
	}
	switch protocol {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses:
		return "bearer"
	case ProtocolAnthropicMessages:
		return "anthropic"
	case ProtocolGeminiGenerateContent:
		return "gemini"
	}
	return ""
}

// newAnthropicAuthenticator builds the x-api-key + anthropic-version
// authenticator used for the "anthropic" auth scheme.
func newAnthropicAuthenticator(credentials json.RawMessage) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(credentials, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return anthropicAuthenticator{apiKey: c.APIKey, version: DefaultAnthropicVersion}, nil
}

// newGeminiContentAuthenticator builds the fixed x-goog-api-key authenticator
// used for the "gemini" auth scheme, with openaiCompatible forced false
// (Vertex/OAuth branching is out of scope) — this is deliberate: a
// gemini-provider upstream is fixed to x-goog-api-key regardless of which
// protocol it's configured with.
func newGeminiContentAuthenticator(credentials json.RawMessage) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(credentials, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return geminiAuthenticator{apiKey: c.APIKey, openaiCompatible: false}, nil
}
