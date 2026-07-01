package provider

import "context"

// CustomProvider implements custom OpenAI-compatible provider behavior. It is
// the fallback for user-defined upstreams that are not a named built-in vendor.
// Config-layer defaulting resolves an empty upstream provider to "custom".
type CustomProvider struct {
	DefaultProvider
}

func init() {
	Register(CustomProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "custom",
			Name:            "Custom",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols:       []Protocol{{ID: ProtocolOpenAICompatible}},
			Models:          ModelDiscovery{Kind: KindStatic},
			Credentials:     CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret"}}},
		},
	}})
}

func (p CustomProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	if len(upstream.CredentialsJSON) == 0 {
		return NoopAuthenticator{}, nil
	}
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
