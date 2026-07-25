package spec

import "testing"

func TestProtocolEndpointString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ep   ProtocolEndpoint
		want string
	}{
		{OpenAIChatCompletionsV1, "openai-chatcompletions/v1"},
		{OpenAIResponsesV1, "openai-responses/v1"},
		{AnthropicMessagesV1, "anthropic-messages/v1"},
		{GeminiGenerateContentV1Beta, "gemini-generatecontent/v1beta"},
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
	// Canonical identifier plus the ONE frozen alias per protocol. This table
	// may gain rows but must not change existing ones — an alias is bound for
	// good, so editing a row is a breaking change to every user's config.
	cases := map[string]Protocol{
		"anthropic-messages":     ProtocolAnthropicMessages,
		"claude":                 ProtocolAnthropicMessages,
		"openai-chatcompletions": ProtocolOpenAIChatCompletions,
		"openai":                 ProtocolOpenAIChatCompletions,
		"openai-embeddings":      ProtocolOpenAIEmbeddings,
		"embed":                  ProtocolOpenAIEmbeddings,
		"openai-responses":       ProtocolOpenAIResponses,
		"codex":                  ProtocolOpenAIResponses,
		"gemini-generatecontent": ProtocolGeminiGenerateContent,
		"gemini":                 ProtocolGeminiGenerateContent,
	}
	for in, want := range cases {
		got, err := ParseProtocol(in)
		if err != nil || got != want {
			t.Errorf("ParseProtocol(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// This is an unreleased schema with no consumers yet, so there is no
	// back-compat alias set — old/dropped identifiers must not resolve. This
	// guards against silently re-accepting a retired spelling as well as
	// against typo tolerance.
	for _, dropped := range []string{
		// retired in the family/api rename
		"openai-chat", "google-gemini",
		// retired mechanically-derived aliases (one alias per protocol now)
		"openai-resp", "openai-embed",
		// never valid
		"responses", "embeddings", "chatcompletions", "generatecontent",
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
		ProtocolAnthropicMessages:     "Anthropic Messages",
		ProtocolOpenAIChatCompletions: "OpenAI Chat Completions",
		ProtocolOpenAIEmbeddings:      "OpenAI Embeddings",
		ProtocolOpenAIResponses:       "OpenAI Responses",
		ProtocolGeminiGenerateContent: "Gemini generateContent",
	}
	for p, want := range cases {
		if got := p.DisplayName(); got == "Unknown" || got == "" {
			t.Errorf("%q.DisplayName() = %q, want a real display name", p, got)
		} else if got != want {
			t.Errorf("%q.DisplayName() = %q, want %q", p, got, want)
		}
	}
}
