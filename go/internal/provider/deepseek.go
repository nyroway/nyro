package provider

import "context"

// DeepSeekProvider implements DeepSeek provider behavior.
type DeepSeekProvider struct {
	DefaultProvider
}

func init() {
	Register(DeepSeekProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "deepseek",
			Name:            "DeepSeek",
			DefaultProtocol: ProtocolOpenAIChatCompletions,
			DefaultModel:    "deepseek-chat",
			Protocols: []Protocol{
				{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://api.deepseek.com/v1"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://api.deepseek.com/anthropic"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://api.deepseek.com/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "DEEPSEEK_API_KEY"}}},
		},
	}})
}

func (p DeepSeekProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
