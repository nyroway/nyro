package provider

// init registers the OpenRouter provider preset: pure configuration data.
// Its outbound authentication (Authorization: Bearer) is dispatched by the
// "bearer" auth scheme in authenticator.go, keyed off Definition.Auth below.
func init() {
	Register(Definition{
		ID:              "openrouter",
		Name:            "OpenRouter",
		Priority:        5,
		DefaultProtocol: ProtocolOpenAIChatCompletions,
		Protocols: []Protocol{
			// All three live under /api/v1: chat/completions, responses, messages.
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://openrouter.ai/api/v1"},
			{ID: ProtocolOpenAIResponses, BaseURL: "https://openrouter.ai/api/v1"},
			{ID: ProtocolAnthropicMessages, BaseURL: "https://openrouter.ai/api/v1"},
		},
		ModelsURL:   "https://openrouter.ai/api/v1/models",
		Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "OPENROUTER_API_KEY"}}},
		Auth:        "bearer",
	})
}
