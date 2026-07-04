package provider

import "context"

// OpenRouterProvider implements OpenRouter provider behavior.
type OpenRouterProvider struct {
	DefaultProvider
}

func init() {
	Register(OpenRouterProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "openrouter",
			Name:            "OpenRouter",
			DefaultProtocol: ProtocolOpenAIChatCompletions,
			Protocols: []Protocol{
				// All three live under /api/v1: chat/completions, responses, messages.
				{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://openrouter.ai/api/v1"},
				{ID: ProtocolOpenAIResponses, BaseURL: "https://openrouter.ai/api/v1"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://openrouter.ai/api/v1"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://openrouter.ai/api/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "OPENROUTER_API_KEY"}}},
		},
	}})
}

func (p OpenRouterProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
