package provider

// init registers the DeepSeek provider preset: pure configuration data. Its
// outbound authentication (Authorization: Bearer) is dispatched by the
// "bearer" auth scheme in authenticator.go, keyed off Definition.Auth below.
func init() {
	Register(Definition{
		ID:              "deepseek",
		Name:            "DeepSeek",
		Priority:        4,
		DefaultProtocol: ProtocolOpenAIChatCompletions,
		DefaultModel:    "deepseek-chat",
		Protocols: []Protocol{
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://api.deepseek.com/v1"},
			{ID: ProtocolAnthropicMessages, BaseURL: "https://api.deepseek.com/anthropic"},
		},
		ModelsURL:   "https://api.deepseek.com/v1/models",
		Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "DEEPSEEK_API_KEY"}}},
		Auth:        "bearer",
	})
}
