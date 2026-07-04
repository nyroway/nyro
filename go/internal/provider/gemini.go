package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// GeminiProvider implements Google AI Studio (generativelanguage.googleapis.com)
// behavior. Auth differs by protocol: gemini-generatecontent uses x-goog-api-key; the
// openai-chatcompletions endpoint uses a Bearer token.
type GeminiProvider struct {
	DefaultProvider
}

func init() {
	Register(GeminiProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "gemini",
			Name:            "Gemini",
			DefaultProtocol: ProtocolGeminiGenerateContent,
			DefaultModel:    "gemini-2.0-flash",
			Protocols: []Protocol{
				{ID: ProtocolGeminiGenerateContent, BaseURL: "https://generativelanguage.googleapis.com"},
				{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
			},
			Models:      ModelDiscovery{Kind: KindDynamic, URL: "https://generativelanguage.googleapis.com/v1beta/openai/models"},
			Credentials: CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: "GEMINI_API_KEY"}}},
		},
	}})
}

type geminiAuthenticator struct {
	apiKey           string
	openaiCompatible bool
}

func (p GeminiProvider) NewAuthenticator(_ context.Context, upstream UpstreamRuntime) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(upstream.CredentialsJSON, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return geminiAuthenticator{apiKey: c.APIKey, openaiCompatible: upstream.Protocol == ProtocolOpenAIChatCompletions}, nil
}

func (a geminiAuthenticator) Apply(_ context.Context, req *http.Request) error {
	if a.openaiCompatible {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	} else {
		req.Header.Set("x-goog-api-key", a.apiKey)
	}
	return nil
}
