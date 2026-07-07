package ids

import "testing"

func TestProtocolEndpointString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ep   ProtocolEndpoint
		want string
	}{
		{OpenAIChatCompletionsV1, "openai-chat/v1"},
		{OpenAIResponsesV1, "openai-responses/v1"},
		{AnthropicMessages20230601, "anthropic-messages/2023-06-01"},
		{GeminiGenerateContentV1Beta, "google-gemini/v1beta"},
		{OpenAIEmbeddingsV1, "openai-embeddings/v1"},
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
		"anthropic-messages": ProtocolAnthropicMessages,
		"claude":             ProtocolAnthropicMessages,
		"openai-chat":        ProtocolOpenAIChatCompletions,
		"openai":             ProtocolOpenAIChatCompletions,
		"openai-embeddings":  ProtocolOpenAIEmbeddings,
		"embed":              ProtocolOpenAIEmbeddings,
		"openai-embed":       ProtocolOpenAIEmbeddings,
		"openai-responses":   ProtocolOpenAIResponses,
		"codex":              ProtocolOpenAIResponses,
		"openai-resp":        ProtocolOpenAIResponses,
		"google-gemini":      ProtocolGeminiGenerateContent,
		"gemini":             ProtocolGeminiGenerateContent,
	}
	for in, want := range cases {
		got, err := ParseProtocol(in)
		if err != nil || got != want {
			t.Errorf("ParseProtocol(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// This is an unreleased schema with no consumers yet, so there is no
	// back-compat alias set — old/dropped identifiers must not resolve.
	for _, dropped := range []string{
		"openai-chatcompletions", "responses", "gemini-generatecontent", "embeddings",
		"openai-compatible", "openai-compat", "openai-resps", "openaix", "geminix",
		"gemini-content", "azure-inference", "anthropic-msgs", "anthropic",
		"google-genai", "google-generative-ai", "google",
	} {
		if _, err := ParseProtocol(dropped); err == nil {
			t.Errorf("ParseProtocol(%q) = nil error, want unknown-protocol error (alias was dropped)", dropped)
		}
	}
	if _, err := ParseProtocol("nope"); err == nil {
		t.Error("expected error for unknown protocol")
	}
}

func TestDisplayNameCoversAllProtocols(t *testing.T) {
	t.Parallel()
	cases := map[Protocol]string{
		ProtocolAnthropicMessages:     "Anthropic Messages API",
		ProtocolOpenAIChatCompletions: "OpenAI Compatible API",
		ProtocolOpenAIEmbeddings:      "OpenAI Embeddings API",
		ProtocolOpenAIResponses:       "OpenAI Responses API",
		ProtocolGeminiGenerateContent: "Google Gemini API",
	}
	for p, want := range cases {
		if got := p.DisplayName(); got == "Unknown" || got == "" {
			t.Errorf("%q.DisplayName() = %q, want a real display name", p, got)
		} else if got != want {
			t.Errorf("%q.DisplayName() = %q, want %q", p, got, want)
		}
	}
}
