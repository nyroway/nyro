package provider

import "context"

// MoonshotAIProvider implements Moonshot AI provider behavior.
type MoonshotAIProvider struct {
	DefaultProvider
}

func init() {
	Register(MoonshotAIProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "moonshotai",
			Name:            "Moonshot AI",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols: []Protocol{
				{ID: ProtocolOpenAICompatible, BaseURL: "https://api.moonshot.ai/v1"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://api.moonshot.ai/anthropic"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://api.moonshot.ai/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "MOONSHOT_API_KEY"}}},
		},
	}})
}

func (p MoonshotAIProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
