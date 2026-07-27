package spec

import "testing"

// The protocol tables themselves are asserted in contract_test.go against
// protocols.json. What is left here is the behaviour around them: the endpoint
// string form, and how the accessors treat input that is not in the catalog.

func TestProtocolEndpointString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ep   ProtocolEndpoint
		want string
	}{
		{OpenAIChatCompletionsV1, "openai-chatcompletions/v1"},
		{OpenAIResponsesV1, "openai-responses/v1"},
		{OpenAIEmbeddingsV1, "openai-embeddings/v1"},
		// v1 from Anthropic's /v1/messages URL, not its anthropic-version
		// header date — see the Version field comment.
		{AnthropicMessagesV1, "anthropic-messages/v1"},
		{GeminiGenerateContentV1Beta, "gemini-generatecontent/v1beta"},
	}
	for _, c := range cases {
		if got := c.ep.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.ep, got, c.want)
		}
	}
}

func TestEveryEndpointHasACatalogEntry(t *testing.T) {
	t.Parallel()
	for _, ep := range []ProtocolEndpoint{
		OpenAIChatCompletionsV1, OpenAIResponsesV1, OpenAIEmbeddingsV1,
		AnthropicMessagesV1, GeminiGenerateContentV1Beta,
	} {
		if _, ok := Lookup(ep.Protocol); !ok {
			t.Errorf("endpoint %s names protocol %q, which is not in the catalog", ep, ep.Protocol)
		}
	}
}

func TestUnknownProtocol(t *testing.T) {
	t.Parallel()
	if _, err := ParseProtocol("nope"); err == nil {
		t.Error("ParseProtocol(\"nope\") = nil error, want unknown-protocol error")
	}
	if _, err := ParseProtocol(""); err == nil {
		t.Error("ParseProtocol(\"\") = nil error, want unknown-protocol error")
	}
	if got := Protocol("nope").DisplayName(); got != "Unknown" {
		t.Errorf("Protocol(\"nope\").DisplayName() = %q, want \"Unknown\"", got)
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup(\"nope\") = _, true; want false")
	}
}
