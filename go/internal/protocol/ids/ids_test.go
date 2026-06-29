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
		{GoogleGeminiGenerateContentV1Beta, "google-gemini/generate-content/v1beta"},
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
		"openai":    ProtocolOpenAICompatible,
		"claude":    ProtocolAnthropicMessages,
		"gemini":    ProtocolGoogleGemini,
		"responses": ProtocolOpenAIResponses,
	}
	for in, want := range cases {
		got, err := ParseProtocol(in)
		if err != nil || got != want {
			t.Errorf("ParseProtocol(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseProtocol("nope"); err == nil {
		t.Error("expected error for unknown protocol")
	}
}
