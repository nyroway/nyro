package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

const DefaultAnthropicVersion = "2023-06-01"

func OpenAI() Registration {
	return registration(Definition{
		ID: "openai", Name: "OpenAI", Priority: 2,
		DefaultProtocol: ProtocolOpenAIChatCompletions, DefaultModel: "gpt-4o-mini",
		Protocols: []Protocol{
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://api.openai.com/v1"},
			{ID: ProtocolOpenAIResponses, BaseURL: "https://api.openai.com/v1"},
		},
		ModelsURL:   "https://api.openai.com/v1/models",
		Credentials: apiKeySchema("OPENAI_API_KEY"),
	}, "bearer")
}

func Anthropic() Registration {
	return registration(Definition{
		ID: "anthropic", Name: "Anthropic", Priority: 1,
		DefaultProtocol: ProtocolAnthropicMessages, DefaultModel: "claude-sonnet-4-6",
		Protocols:   []Protocol{{ID: ProtocolAnthropicMessages, BaseURL: "https://api.anthropic.com"}},
		ModelsURL:   "https://api.anthropic.com/v1/models",
		Credentials: apiKeySchema("ANTHROPIC_API_KEY"),
		Extra:       map[string]any{"anthropic_version": DefaultAnthropicVersion},
	}, "anthropic")
}

func Gemini() Registration {
	return registration(Definition{
		ID: "gemini", Name: "Gemini", Priority: 3,
		DefaultProtocol: ProtocolGeminiGenerateContent, DefaultModel: "gemini-2.0-flash",
		Protocols: []Protocol{
			{ID: ProtocolGeminiGenerateContent, BaseURL: "https://generativelanguage.googleapis.com"},
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
		},
		ModelsURL:   "https://generativelanguage.googleapis.com/v1beta/openai/models",
		Credentials: apiKeySchema("GEMINI_API_KEY"),
	}, "gemini")
}

func DeepSeek() Registration {
	return registration(Definition{
		ID: "deepseek", Name: "DeepSeek", Priority: 4,
		DefaultProtocol: ProtocolOpenAIChatCompletions, DefaultModel: "deepseek-chat",
		Protocols: []Protocol{
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://api.deepseek.com/v1"},
			{ID: ProtocolAnthropicMessages, BaseURL: "https://api.deepseek.com/anthropic"},
		},
		ModelsURL:   "https://api.deepseek.com/v1/models",
		Credentials: apiKeySchema("DEEPSEEK_API_KEY"),
	}, "bearer")
}

func OpenRouter() Registration {
	return registration(Definition{
		ID: "openrouter", Name: "OpenRouter", Priority: 5,
		DefaultProtocol: ProtocolOpenAIChatCompletions,
		Protocols: []Protocol{
			{ID: ProtocolOpenAIChatCompletions, BaseURL: "https://openrouter.ai/api/v1"},
			{ID: ProtocolOpenAIResponses, BaseURL: "https://openrouter.ai/api/v1"},
			{ID: ProtocolAnthropicMessages, BaseURL: "https://openrouter.ai/api/v1"},
		},
		ModelsURL:   "https://openrouter.ai/api/v1/models",
		Credentials: apiKeySchema("OPENROUTER_API_KEY"),
	}, "bearer")
}

func Generic() Registration {
	registration := registration(Definition{ID: "generic", Name: "Generic"}, "")
	registration.Fallback = true
	return registration
}

func registration(definition Definition, authScheme string) Registration {
	return Registration{
		Definition: definition,
		Factory: func() Driver {
			return standardDriver{authScheme: authScheme}
		},
	}
}

func apiKeySchema(env string) CredentialSchema {
	return CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "secret", Required: true, Env: env}}}
}

type standardDriver struct {
	authScheme string
}

func (standardDriver) ExtendRequest(context.Context, UpstreamRuntime, llm.ModelRequest) error {
	return nil
}

func (driver standardDriver) Prepare(_ context.Context, upstream UpstreamRuntime, wire protocol.WireRequest) (Request, error) {
	headers := make(map[string]string, len(wire.Headers)+2)
	for key, value := range wire.Headers {
		headers[key] = value
	}
	if _, ok := headers["Content-Type"]; !ok && len(wire.Body) > 0 {
		headers["Content-Type"] = "application/json"
	}
	if err := applyAuthentication(headers, driver.authenticationScheme(upstream.Protocol), upstream.CredentialsJSON); err != nil {
		return Request{}, err
	}
	return Request{
		Method: wire.Method, URL: BuildURL(upstream.BaseURL, wire.Path), Headers: headers,
		Body: append([]byte(nil), wire.Body...), Stream: wire.Stream, ProxyURL: upstream.ProxyURL,
	}, nil
}

func (driver standardDriver) Classify(response Response) Classification {
	return Classification{Failed: response.StatusCode >= 400}
}

func (standardDriver) ExtendResponse(context.Context, UpstreamRuntime, *llm.ChatResponse) error {
	return nil
}

func (standardDriver) ExtendError(context.Context, UpstreamRuntime, *llm.Error) error {
	return nil
}

func (driver standardDriver) authenticationScheme(protocolID string) string {
	if driver.authScheme != "" {
		return driver.authScheme
	}
	if parsed, err := protocol.ParseProtocol(protocolID); err == nil {
		protocolID = parsed.String()
	}
	switch protocolID {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses:
		return "bearer"
	case ProtocolAnthropicMessages:
		return "anthropic"
	case ProtocolGeminiGenerateContent:
		return "gemini"
	default:
		return ""
	}
}

func applyAuthentication(headers map[string]string, scheme string, credentials json.RawMessage) error {
	if scheme == "" && len(credentials) == 0 {
		return nil
	}
	var parsed struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(credentials, &parsed); err != nil {
		return err
	}
	if parsed.APIKey == "" {
		return errors.New("provider: api_key is required")
	}
	switch scheme {
	case "anthropic":
		headers["x-api-key"] = parsed.APIKey
		headers["anthropic-version"] = DefaultAnthropicVersion
	case "gemini":
		headers["x-goog-api-key"] = parsed.APIKey
	default:
		headers["Authorization"] = "Bearer " + parsed.APIKey
	}
	return nil
}

func CredentialSchemaFor(protocolID string) CredentialSchema {
	protocolID = strings.TrimSpace(protocolID)
	switch protocolID {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerateContent:
		return CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "string", Required: true}}}
	default:
		return CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "string"}}}
	}
}
