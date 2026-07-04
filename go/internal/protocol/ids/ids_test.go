package ids

import "testing"

func TestProtocolEndpointString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ep   ProtocolEndpoint
		want string
	}{
		{OpenAICompatibleChatCompletionsV1, "openai-compatible/chat-completions/v1"},
		{OpenAIResponsesV1, "openai-responses/responses/v1"},
		{AnthropicMessages20230601, "anthropic-messages/messages/2023-06-01"},
		{GeminiContentV1Beta, "gemini-content/generate-content/v1beta"},
		{OpenAICompatibleEmbeddingsV1, "openai-compatible/embeddings/v1"},
	}
	for _, c := range cases {
		if got := c.ep.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.ep, got, c.want)
		}
	}
}

func TestParseProtocolAliases(t *testing.T) {
	t.Parallel()
	cases := map[string]Protocol{
		"anthropic-messages":  ProtocolAnthropicMessages,
		"claude":              ProtocolAnthropicMessages,
		"openai-compatible":   ProtocolOpenAICompatible,
		"openai":              ProtocolOpenAICompatible,
		"openai-responses":    ProtocolOpenAIResponses,
		"openaix":             ProtocolOpenAIResponses,
		"gemini-content":      ProtocolGeminiContent,
		"gemini":              ProtocolGeminiContent,
		"gemini-interactions": ProtocolGeminiInteractions,
		"geminix":             ProtocolGeminiInteractions,
		"bedrock-converse":    ProtocolBedrockConverse,
		"bedrock":             ProtocolBedrockConverse,
		"azure-inference":     ProtocolAzureInference,
		"azure":               ProtocolAzureInference,
	}
	for in, want := range cases {
		got, err := ParseProtocol(in)
		if err != nil || got != want {
			t.Errorf("ParseProtocol(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// Old, now-dropped aliases must not silently resolve — this schema has no
	// back-compat alias set.
	for _, dropped := range []string{"openai-compat", "openai-resps", "responses", "anthropic-msgs", "anthropic", "google-genai", "google-generative-ai", "google"} {
		if _, err := ParseProtocol(dropped); err == nil {
			t.Errorf("ParseProtocol(%q) = nil error, want unknown-protocol error (alias was dropped)", dropped)
		}
	}
	if _, err := ParseProtocol("nope"); err == nil {
		t.Error("expected error for unknown protocol")
	}
}

func TestNameAndFullNameCoverAllProtocols(t *testing.T) {
	t.Parallel()
	for _, p := range []Protocol{
		ProtocolAnthropicMessages, ProtocolOpenAICompatible, ProtocolOpenAIResponses,
		ProtocolGeminiContent, ProtocolGeminiInteractions, ProtocolBedrockConverse, ProtocolAzureInference,
	} {
		if got := p.Name(); got == "Unknown" || got == "" {
			t.Errorf("%q.Name() = %q, want a real short name", p, got)
		}
		if got := p.FullName(); got == "Unknown" || got == "" {
			t.Errorf("%q.FullName() = %q, want a real full name", p, got)
		}
	}
}
