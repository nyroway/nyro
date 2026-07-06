package provider

// defaultAnthropicVersion is the anthropic-version header value used when the
// Definition.Extra override is absent.
const defaultAnthropicVersion = "2023-06-01"

// init registers the Anthropic provider preset: pure configuration data. Its
// outbound authentication (x-api-key + anthropic-version, not Bearer) is
// dispatched by the "anthropic" auth scheme in authenticator.go, keyed off
// Definition.Auth below (anthropicAuthenticator lives in authenticator.go).
func init() {
	Register(Definition{
		ID:              "anthropic",
		Name:            "Anthropic",
		Priority:        1,
		DefaultProtocol: ProtocolAnthropicMessages,
		DefaultModel:    "claude-sonnet-4-6",
		Protocols:       []Protocol{{ID: ProtocolAnthropicMessages, BaseURL: "https://api.anthropic.com"}},
		ModelsURL:       "https://api.anthropic.com/v1/models",
		Credentials:     CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "ANTHROPIC_API_KEY"}}},
		Extra:           map[string]any{"anthropic_version": defaultAnthropicVersion},
		Auth:            "anthropic",
	})
}
