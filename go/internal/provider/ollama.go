package provider

import "context"

// OllamaProvider implements Ollama (local) provider behavior. API key is
// optional: when absent, requests are sent unauthenticated.
type OllamaProvider struct {
	DefaultProvider
}

func init() {
	Register(OllamaProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "ollama",
			Name:            "Ollama",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols: []Protocol{
				// All three live under /v1: chat/completions, responses, messages.
				{ID: ProtocolOpenAICompatible, BaseURL: "http://127.0.0.1:11434/v1"},
				{ID: ProtocolOpenAIResponses, BaseURL: "http://127.0.0.1:11434/v1"},
				{ID: ProtocolAnthropicMessages, BaseURL: "http://127.0.0.1:11434/v1"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "http://127.0.0.1:11434/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret"}}},
		},
	}})
}

func (p OllamaProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	if len(upstream.CredentialsJSON) == 0 {
		return NoopAuthenticator{}, nil
	}
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
