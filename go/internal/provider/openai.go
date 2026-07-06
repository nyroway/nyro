package provider

// init registers the OpenAI provider preset: pure configuration data. Its
// outbound authentication (Authorization: Bearer) is dispatched by the
// "bearer" auth scheme in authenticator.go, keyed off Definition.Auth below.
func init() {
	Register(Definition{
		ID:              "openai",
		Name:            "OpenAI",
		Priority:        2,
		DefaultProtocol: ProtocolOpenAIChatCompletions,
		DefaultModel:    "gpt-4o-mini",
		Protocols: []Protocol{
			// Both chat/completions and responses live under /v1.
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://api.openai.com/v1"},
			{ID: ProtocolOpenAIResponses, BaseURL: "https://api.openai.com/v1"},
		},
		ModelsURL:   "https://api.openai.com/v1/models",
		Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "OPENAI_API_KEY"}}},
		Auth:        "bearer",
	})
}
