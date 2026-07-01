package provider

import "context"

// ZAIProvider implements Z.ai provider behavior.
type ZAIProvider struct {
	DefaultProvider
}

func init() {
	Register(ZAIProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "zai",
			Name:            "Z.ai",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols: []Protocol{
				{ID: ProtocolOpenAICompatible, BaseURL: "https://api.z.ai/api/paas/v4"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://api.z.ai/api/anthropic"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://api.z.ai/api/paas/v4/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "ZAI_API_KEY"}}},
		},
	}})
}

func (p ZAIProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
