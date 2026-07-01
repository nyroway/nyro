package provider

import "context"

// XAIProvider implements xAI (Grok) provider behavior.
type XAIProvider struct {
	DefaultProvider
}

func init() {
	Register(XAIProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "xai",
			Name:            "xAI",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols:       []Protocol{{ID: ProtocolOpenAICompatible, BaseURL: "https://api.x.ai/v1"}},
			Models:          ModelDiscovery{Kind: KindDynamic, URL: "https://api.x.ai/v1/models"},
			Credentials:     CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "XAI_API_KEY"}}},
		},
	}})
}

func (p XAIProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
