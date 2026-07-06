package provider

// init registers the Google AI Studio (generativelanguage.googleapis.com)
// provider preset: pure configuration data. Its outbound authentication is
// fixed to x-goog-api-key regardless of which protocol the upstream speaks
// (see newGeminiContentAuthenticator / geminiAuthenticator in
// authenticator.go), dispatched by the "gemini" auth scheme keyed off
// Definition.Auth below.
func init() {
	Register(Definition{
		ID:              "gemini",
		Name:            "Gemini",
		Priority:        3,
		DefaultProtocol: ProtocolGeminiGenerateContent,
		DefaultModel:    "gemini-2.0-flash",
		Protocols: []Protocol{
			{ID: ProtocolGeminiGenerateContent, BaseURL: "https://generativelanguage.googleapis.com"},
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
		},
		ModelsURL:   "https://generativelanguage.googleapis.com/v1beta/openai/models",
		Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "GEMINI_API_KEY"}}},
		Auth:        "gemini",
	})
}
