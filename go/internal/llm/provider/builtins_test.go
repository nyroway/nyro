package provider

import "testing"

func TestBuiltinDefinitionsPreserveProviderMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		registration    Registration
		defaultProtocol string
		defaultModel    string
		protocols       []string
		modelsURL       string
	}{
		{OpenAI(), ProtocolOpenAIChatCompletions, "gpt-4o-mini", []string{ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses}, "https://api.openai.com/v1/models"},
		{Anthropic(), ProtocolAnthropicMessages, "claude-sonnet-4-6", []string{ProtocolAnthropicMessages}, "https://api.anthropic.com/v1/models"},
		{Gemini(), ProtocolGeminiGenerateContent, "gemini-2.0-flash", []string{ProtocolGeminiGenerateContent, ProtocolOpenAIChatCompletions}, "https://generativelanguage.googleapis.com/v1beta/openai/models"},
		{DeepSeek(), ProtocolOpenAIChatCompletions, "deepseek-chat", []string{ProtocolOpenAIChatCompletions, ProtocolAnthropicMessages}, "https://api.deepseek.com/v1/models"},
		{OpenRouter(), ProtocolOpenAIChatCompletions, "", []string{ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages}, "https://openrouter.ai/api/v1/models"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.registration.Definition.ID, func(t *testing.T) {
			t.Parallel()
			definition := test.registration.Definition
			if definition.DefaultProtocol != test.defaultProtocol || definition.DefaultModel != test.defaultModel {
				t.Fatalf("defaults = (%q, %q), want (%q, %q)", definition.DefaultProtocol, definition.DefaultModel, test.defaultProtocol, test.defaultModel)
			}
			if definition.ModelsURL != test.modelsURL {
				t.Fatalf("ModelsURL = %q, want %q", definition.ModelsURL, test.modelsURL)
			}
			for _, protocolID := range test.protocols {
				if !SupportsProtocol(definition, protocolID) {
					t.Errorf("definition does not support %q", protocolID)
				}
			}
			if len(definition.Credentials.Fields) != 1 || definition.Credentials.Fields[0].Name != "api_key" {
				t.Fatalf("credential fields = %+v", definition.Credentials.Fields)
			}
		})
	}
}

func TestCredentialSchemaForKnownAndUnknownProtocols(t *testing.T) {
	t.Parallel()
	for _, protocolID := range []string{
		ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses,
		ProtocolAnthropicMessages, ProtocolGeminiGenerateContent,
	} {
		field := CredentialSchemaFor(protocolID).Fields[0]
		if field.Name != "api_key" || field.Type != "string" || !field.Required {
			t.Errorf("CredentialSchemaFor(%q) = %+v", protocolID, field)
		}
	}
	field := CredentialSchemaFor("custom").Fields[0]
	if field.Name != "api_key" || field.Type != "string" || field.Required {
		t.Fatalf("CredentialSchemaFor(custom) = %+v", field)
	}
}

func TestBuildURLAvoidsDuplicateVersionSegment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		path string
		want string
	}{
		{"https://example.com/v1", "/v1/chat/completions", "https://example.com/v1/chat/completions"},
		{"https://example.com", "/v1/chat/completions", "https://example.com/v1/chat/completions"},
		{"https://example.com/api/v1", "/messages", "https://example.com/api/v1/messages"},
	}
	for _, test := range tests {
		if got := BuildURL(test.base, test.path); got != test.want {
			t.Errorf("BuildURL(%q, %q) = %q, want %q", test.base, test.path, got, test.want)
		}
	}
}
