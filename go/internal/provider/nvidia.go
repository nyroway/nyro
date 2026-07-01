package provider

import "context"

// NVIDIAProvider implements NVIDIA NIM provider behavior.
type NVIDIAProvider struct {
	DefaultProvider
}

func init() {
	Register(NVIDIAProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "nvidia",
			Name:            "NVIDIA",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols:       []Protocol{{ID: ProtocolOpenAICompatible, BaseURL: "https://integrate.api.nvidia.com/v1"}},
			Models:          ModelDiscovery{Kind: KindDynamic, URL: "https://integrate.api.nvidia.com/v1/models"},
			Credentials:     CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "NVIDIA_API_KEY"}}},
		},
	}})
}

func (p NVIDIAProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
