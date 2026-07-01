package provider

import "context"

// MiniMaxProvider implements MiniMax provider behavior.
type MiniMaxProvider struct {
	DefaultProvider
}

func init() {
	Register(MiniMaxProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "minimax",
			Name:            "MiniMax",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols: []Protocol{
				{ID: ProtocolOpenAICompatible, BaseURL: "https://api.minimax.io/v1"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://api.minimax.io/anthropic"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://api.minimax.io/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "MINIMAX_API_KEY"}}},
		},
	}})
}

func (p MiniMaxProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
