package provider

import "context"

// ZhipuAIProvider implements Zhipu AI provider behavior.
type ZhipuAIProvider struct {
	DefaultProvider
}

func init() {
	Register(ZhipuAIProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "zhipuai",
			Name:            "Zhipu AI",
			DefaultProtocol: ProtocolOpenAICompatible,
			Protocols: []Protocol{
				{ID: ProtocolOpenAICompatible, BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
				{ID: ProtocolAnthropicMessages, BaseURL: "https://open.bigmodel.cn/api/anthropic"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://open.bigmodel.cn/api/paas/v4/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "ZHIPUAI_API_KEY"}}},
		},
	}})
}

func (p ZhipuAIProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	return NewBearerAuthenticator(upstream.CredentialsJSON)
}
