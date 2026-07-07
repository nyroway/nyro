// Package ids defines the two-layer protocol identity used across the
// gateway: Protocol (a single concrete wire-format API surface) and
// ProtocolEndpoint (that protocol at a specific version).
//
// Canonical string form: "{protocol}/{version}"
// (e.g. "openai-chat/v1").
//
// Ported from crates/nyro-core/src/protocol/ids.rs. EndpointCapabilities and
// StreamCaps (also in ids.rs) describe codec/negotiator behaviour and are
// ported alongside that layer.
package ids

import "fmt"

// Protocol is a single concrete wire-format API surface — one logical
// operation (chat, embeddings, generate-content, ...) with one request/
// response shape. It is orthogonal to Vendor — multiple vendors (OpenAI,
// Moonshot, DeepSeek, ...) may implement the same Protocol.
//
// A protocol ID is independent of transport (authentication, URL structure,
// query parameters), which is owned by the provider's Authenticator and URL
// construction.
//
// Identifier | Display Name | Alias:
//
//	anthropic-messages   | Anthropic Messages API        | claude
//	openai-chat          | OpenAI Compatible API         | openai
//	openai-responses     | OpenAI Responses API          | codex, openai-resp
//	openai-embeddings    | OpenAI Embeddings API         | embed, openai-embed
//	google-gemini        | Google Gemini API             | gemini
//
// Cloud protocol routing — which protocol to use for a given model on each cloud:
//
//	AWS Bedrock (SigV4 auth throughout):
//	  - Claude            → anthropic-messages  (InvokeModel; adds anthropic_version="bedrock-*", model in URL)
//	  - any model (unify) → Converse API (cross-model unified schema; no protocol declared yet)
//
//	Azure (api-key header or Azure AD):
//	  - OpenAI GPT/o (Azure OpenAI Service) → AI Model Inference API (deployment in path, api-version query; no protocol declared yet)
//	  - Claude (AI Foundry serverless)      → anthropic-messages     (Foundry anthropic endpoint)
//	  - Foundry non-Claude (Llama/Mistral)  → openai-chat (AI Model Inference API)
//
//	GCP Vertex AI (OAuth / service-account):
//	  - Gemini            → google-gemini  (generateContent)
//	  - Claude            → anthropic-messages       (rawPredict; model in path)
//	  - some 3rd-party    → openai-chat   (/endpoints/openapi; partial coverage)
//	  - other 3rd-party   → publisher-native via rawPredict (no unified layer)
//
// anthropic-messages is the common denominator: Claude on all three clouds
// accepts the anthropic Messages body — only the transport differs.
type Protocol string

const (
	ProtocolAnthropicMessages     Protocol = "anthropic-messages"
	ProtocolOpenAIChatCompletions Protocol = "openai-chat"
	// ProtocolOpenAIEmbeddings is split out of the old openai-compatible
	// family; not exposed as a selectable protocol yet.
	ProtocolOpenAIEmbeddings      Protocol = "openai-embeddings"
	ProtocolOpenAIResponses       Protocol = "openai-responses"
	ProtocolGeminiGenerateContent Protocol = "google-gemini"
)

// String returns the canonical kebab-case identifier.
func (p Protocol) String() string { return string(p) }

// DisplayName returns the display label for a protocol (e.g. "Anthropic Messages
// API").
func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "Anthropic Messages API"
	case ProtocolOpenAIChatCompletions:
		return "OpenAI Compatible API"
	case ProtocolOpenAIEmbeddings:
		return "OpenAI Embeddings API"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses API"
	case ProtocolGeminiGenerateContent:
		return "Google Gemini API"
	}
	return "Unknown"
}

// ParseProtocol resolves a canonical string or its short alias to a Protocol.
func ParseProtocol(s string) (Protocol, error) {
	switch s {
	case "anthropic-messages", "claude":
		return ProtocolAnthropicMessages, nil
	case "openai-chat", "openai":
		return ProtocolOpenAIChatCompletions, nil
	case "openai-embeddings", "embed", "openai-embed":
		return ProtocolOpenAIEmbeddings, nil
	case "openai-responses", "codex", "openai-resp":
		return ProtocolOpenAIResponses, nil
	case "google-gemini", "gemini":
		return ProtocolGeminiGenerateContent, nil
	}
	return "", fmt.Errorf("unknown protocol: %s", s)
}

// ProtocolEndpoint is a Protocol at a specific wire-format version.
//
// Canonical display: "{protocol}/{version}".
type ProtocolEndpoint struct {
	Protocol Protocol
	// Version is the wire-format version string as the vendor labels it.
	Version string
}

// String returns the canonical "{protocol}/{version}" form.
func (e ProtocolEndpoint) String() string {
	return fmt.Sprintf("%s/%s", e.Protocol, e.Version)
}

// Canonical ProtocolEndpoint values.
var (
	OpenAIChatCompletionsV1     = ProtocolEndpoint{ProtocolOpenAIChatCompletions, "v1"}
	OpenAIEmbeddingsV1          = ProtocolEndpoint{ProtocolOpenAIEmbeddings, "v1"}
	OpenAIResponsesV1           = ProtocolEndpoint{ProtocolOpenAIResponses, "v1"}
	AnthropicMessages20230601   = ProtocolEndpoint{ProtocolAnthropicMessages, "2023-06-01"}
	GeminiGenerateContentV1Beta = ProtocolEndpoint{ProtocolGeminiGenerateContent, "v1beta"}
)

// ProtocolID is a backward-compat alias; prefer ProtocolEndpoint.
// (Matches the Rust `pub type ProtocolId = ProtocolEndpoint`.)
type ProtocolID = ProtocolEndpoint

// ChatEndpointFor returns the default chat/generate endpoint for a protocol
// suite. Used by the dispatcher to resolve the egress codec for cross-protocol
// routing (e.g. an Anthropic client hitting an OpenAI-compatible provider).
func ChatEndpointFor(p Protocol) (ProtocolEndpoint, bool) {
	switch p {
	case ProtocolOpenAIChatCompletions:
		return OpenAIChatCompletionsV1, true
	case ProtocolOpenAIResponses:
		return OpenAIResponsesV1, true
	case ProtocolAnthropicMessages:
		return AnthropicMessages20230601, true
	case ProtocolGeminiGenerateContent:
		return GeminiGenerateContentV1Beta, true
	}
	return ProtocolEndpoint{}, false
}
