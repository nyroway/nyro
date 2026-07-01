package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// defaultAnthropicVersion is the anthropic-version header value used when the
// Definition.Extra override is absent.
const defaultAnthropicVersion = "2023-06-01"

// AnthropicProvider implements Anthropic provider behavior. It authenticates
// with an x-api-key header plus the anthropic-version header (not Bearer).
type AnthropicProvider struct {
	DefaultProvider
}

func init() {
	Register(AnthropicProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "anthropic",
			Name:            "Anthropic",
			DefaultProtocol: ProtocolAnthropicMessages,
			DefaultModel:    "claude-sonnet-4-6",
			Protocols:       []Protocol{{ID: ProtocolAnthropicMessages, BaseURL: "https://api.anthropic.com"}},
			Models:          ModelDiscovery{Kind: KindDynamic, URL: "https://api.anthropic.com/v1/models"},
			Credentials:     CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "ANTHROPIC_API_KEY"}}},
			Extra:           map[string]any{"anthropic_version": defaultAnthropicVersion},
		},
	}})
}

type anthropicAuthenticator struct {
	apiKey  string
	version string
}

func (p AnthropicProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(upstream.CredentialsJSON, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	version := defaultAnthropicVersion
	if v, ok := p.Def.Extra["anthropic_version"].(string); ok && v != "" {
		version = v
	}
	return anthropicAuthenticator{apiKey: c.APIKey, version: version}, nil
}

func (a anthropicAuthenticator) Apply(_ context.Context, req *http.Request) error {
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", a.version)
	return nil
}
