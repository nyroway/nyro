// Package ids defines the three-layer protocol identity used across the
// gateway: Protocol (the wire-format suite) and ProtocolEndpoint (a specific
// API endpoint).
//
// Canonical string form: "{protocol}/{name}/{version}"
// (e.g. "openai-chatcompletions/chat-completions/v1").
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
//
// A protocol ID is independent of transport (authentication, URL structure,
// query parameters), which is owned by the provider's Authenticator and URL
// construction.
//
// Identifier | Display Name | Alias:
//
//	anthropic-messages      | Anthropic Messages API          | claude
//	openai-chatcompletions  | OpenAI Chat Completions API      | openai
//	openai-responses        | OpenAI Responses API             | openaix
//	gemini-generatecontent  | Gemini generateContent API       | gemini
//	gemini-interactions     | Gemini Interactions API          | geminix
//	bedrock-converse        | AWS Bedrock Converse API         | bedrock
//	azure-modelinference    | Azure AI Model Inference API     | azure
//
// gemini-interactions, bedrock-converse, and azure-modelinference are
// declared only (ParseProtocol/DisplayName recognize them, provider
// definitions may reference them as defaults) — no codec is registered for
// them yet.
//
// Cloud protocol routing — which protocol to use for a given model on each cloud:
//
//	AWS Bedrock (SigV4 auth throughout):
//	  - Claude            → anthropic-messages  (InvokeModel; adds anthropic_version="bedrock-*", model in URL)
//	  - any model (unify) → bedrock-converse    (Converse API; cross-model unified schema)
//
//	Azure (api-key header or Azure AD):
//	  - OpenAI GPT/o (Azure OpenAI Service) → azure-modelinference   (deployment in path, api-version query)
//	  - Claude (AI Foundry serverless)      → anthropic-messages     (Foundry anthropic endpoint)
//	  - Foundry non-Claude (Llama/Mistral)  → openai-chatcompletions (AI Model Inference API)
//
//	GCP Vertex AI (OAuth / service-account):
//	  - Gemini            → gemini-generatecontent  (generateContent)
//	  - Claude            → anthropic-messages      (rawPredict; model in path)
//	  - some 3rd-party    → openai-chatcompletions  (/endpoints/openapi; partial coverage)
//	  - other 3rd-party   → publisher-native via rawPredict (no unified layer)
//
// anthropic-messages is the common denominator: Claude on all three clouds
// accepts the anthropic Messages body — only the transport differs.
type Protocol string

const (
	ProtocolAnthropicMessages     Protocol = "anthropic-messages"
	ProtocolOpenAIChatCompletions Protocol = "openai-chatcompletions"
	ProtocolOpenAIResponses       Protocol = "openai-responses"
	ProtocolGeminiGenerateContent Protocol = "gemini-generatecontent"
	// Transport-specific or not-yet-implemented protocols; no codec is
	// registered for these yet — they exist so provider definitions can
	// declare them as defaults, and ParseProtocol/DisplayName recognize them.
	ProtocolGeminiInteractions  Protocol = "gemini-interactions"
	ProtocolBedrockConverse     Protocol = "bedrock-converse"
	ProtocolAzureModelInference Protocol = "azure-modelinference"
)

// String returns the canonical kebab-case identifier.
func (p Protocol) String() string { return string(p) }

// DisplayName returns a human-friendly label.
func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "Anthropic Messages API"
	case ProtocolOpenAIChatCompletions:
		return "OpenAI Chat Completions API"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses API"
	case ProtocolGeminiGenerateContent:
		return "Gemini generateContent API"
	case ProtocolGeminiInteractions:
		return "Gemini Interactions API"
	case ProtocolBedrockConverse:
		return "AWS Bedrock Converse API"
	case ProtocolAzureModelInference:
		return "Azure AI Model Inference API"
	}
	return "Unknown"
}

// ParseProtocol resolves a canonical string or its single alias to a
// Protocol. Each protocol has exactly one short alias (see the package
// table); there is no legacy/back-compat alias set — this schema has no
// released consumers yet.
func ParseProtocol(s string) (Protocol, error) {
	switch s {
	case "anthropic-messages", "claude":
		return ProtocolAnthropicMessages, nil
	case "openai-chatcompletions", "openai":
		return ProtocolOpenAIChatCompletions, nil
	case "openai-responses", "openaix":
		return ProtocolOpenAIResponses, nil
	case "gemini-generatecontent", "gemini":
		return ProtocolGeminiGenerateContent, nil
	case "gemini-interactions", "geminix":
		return ProtocolGeminiInteractions, nil
	case "bedrock-converse", "bedrock":
		return ProtocolBedrockConverse, nil
	case "azure-modelinference", "azure":
		return ProtocolAzureModelInference, nil
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
	OpenAIChatCompletionsV1     = ProtocolEndpoint{ProtocolOpenAIChatCompletions, "chat-completions", "v1"}
	OpenAIEmbeddingsV1          = ProtocolEndpoint{ProtocolOpenAIChatCompletions, "embeddings", "v1"}
	OpenAIResponsesV1           = ProtocolEndpoint{ProtocolOpenAIResponses, "responses", "v1"}
	AnthropicMessages20230601   = ProtocolEndpoint{ProtocolAnthropicMessages, "messages", "2023-06-01"}
	GeminiGenerateContentV1Beta = ProtocolEndpoint{ProtocolGeminiGenerateContent, "generate-content", "v1beta"}
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
