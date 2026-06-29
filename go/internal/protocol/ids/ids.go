// Package ids defines the three-layer protocol identity used across the
// gateway: Protocol (the wire-format suite) and ProtocolEndpoint (a specific
// API endpoint).
//
// Canonical string form: "{protocol}/{name}/{version}"
// (e.g. "openai-compatible/chat-completions/v1").
//
// Ported from crates/nyro-core/src/protocol/ids.rs. EndpointCapabilities and
// StreamCaps (also in ids.rs) describe codec/negotiator behaviour and are
// ported alongside that layer.
package ids

import "fmt"

// Protocol is a top-level protocol suite (wire-format family). A Protocol
// groups one or more ProtocolEndpoints that share the same request/response
// wire format. It is orthogonal to Vendor — multiple vendors (OpenAI,
// Moonshot, DeepSeek, ...) may implement the same Protocol.
type Protocol string

const (
	ProtocolOpenAICompatible  Protocol = "openai-compatible"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolGoogleGemini      Protocol = "google-gemini"
)

// String returns the canonical kebab-case identifier.
func (p Protocol) String() string { return string(p) }

// DisplayName returns a human-friendly label.
func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolOpenAICompatible:
		return "OpenAI Compatible"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses"
	case ProtocolAnthropicMessages:
		return "Anthropic Messages"
	case ProtocolGoogleGemini:
		return "Google Gemini"
	}
	return "Unknown"
}

// ParseProtocol resolves a canonical string or common alias to a Protocol.
func ParseProtocol(s string) (Protocol, error) {
	switch s {
	case "openai-compatible", "openai-compat", "openai":
		return ProtocolOpenAICompatible, nil
	case "openai-responses", "openai-resps", "responses":
		return ProtocolOpenAIResponses, nil
	case "anthropic-messages", "anthropic-msgs", "anthropic", "claude":
		return ProtocolAnthropicMessages, nil
	case "google-gemini", "google-genai", "google-generative-ai", "gemini", "google":
		return ProtocolGoogleGemini, nil
	}
	return "", fmt.Errorf("unknown protocol: %s", s)
}

// ProtocolEndpoint is a specific API endpoint within a Protocol.
//
// Canonical display: "{protocol}/{name}/{version}".
type ProtocolEndpoint struct {
	Protocol Protocol
	// Name is the endpoint name (kebab-case, matches the final path segment of
	// the ingress route).
	Name string
	// Version is the wire-format version string as the vendor labels it.
	Version string
}

// String returns the canonical "{protocol}/{name}/{version}" form.
func (e ProtocolEndpoint) String() string {
	return fmt.Sprintf("%s/%s/%s", e.Protocol, e.Name, e.Version)
}

// Canonical ProtocolEndpoint values.
var (
	OpenAICompatibleChatCompletionsV1 = ProtocolEndpoint{ProtocolOpenAICompatible, "chat-completions", "v1"}
	OpenAICompatibleEmbeddingsV1      = ProtocolEndpoint{ProtocolOpenAICompatible, "embeddings", "v1"}
	OpenAIResponsesV1                 = ProtocolEndpoint{ProtocolOpenAIResponses, "responses", "v1"}
	AnthropicMessages20230601         = ProtocolEndpoint{ProtocolAnthropicMessages, "messages", "2023-06-01"}
	GoogleGeminiGenerateContentV1Beta = ProtocolEndpoint{ProtocolGoogleGemini, "generate-content", "v1beta"}
)

// ProtocolID is a backward-compat alias; prefer ProtocolEndpoint.
// (Matches the Rust `pub type ProtocolId = ProtocolEndpoint`.)
type ProtocolID = ProtocolEndpoint

// ChatEndpointFor returns the default chat/generate endpoint for a protocol
// suite. Used by the dispatcher to resolve the egress codec for cross-protocol
// routing (e.g. an Anthropic client hitting an OpenAI-compatible provider).
func ChatEndpointFor(p Protocol) (ProtocolEndpoint, bool) {
	switch p {
	case ProtocolOpenAICompatible:
		return OpenAICompatibleChatCompletionsV1, true
	case ProtocolOpenAIResponses:
		return OpenAIResponsesV1, true
	case ProtocolAnthropicMessages:
		return AnthropicMessages20230601, true
	case ProtocolGoogleGemini:
		return GoogleGeminiGenerateContentV1Beta, true
	}
	return ProtocolEndpoint{}, false
}
