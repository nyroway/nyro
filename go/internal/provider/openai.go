package provider

import "context"

// OpenAIProvider implements OpenAI provider behavior.
type OpenAIProvider struct {
	DefaultProvider
}

func init() {
	Register(OpenAIProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "openai",
			Name:            "OpenAI",
			DefaultProtocol: ProtocolOpenAICompatible,
			DefaultModel:    "gpt-4o-mini",
			Protocols: []Protocol{
				// Both chat/completions and responses live under /v1.
				{ID: ProtocolOpenAICompatible, BaseURL: "https://api.openai.com/v1"},
				{ID: ProtocolOpenAIResponses, BaseURL: "https://api.openai.com/v1"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://api.openai.com/v1/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "OPENAI_API_KEY"}}},
		},
	}})
}

func (p OpenAIProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
