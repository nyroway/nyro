// Package ids defines the two-layer protocol identity used across the
// gateway: Protocol (a single concrete wire-format API surface) and
// ProtocolEndpoint (that protocol at a specific version).
//
// Canonical string form: "{protocol}/{version}"
// (e.g. "openai-chatcompletions/v1").
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
// Identifier | Name (short) | FullName | Alias:
//
//	anthropic-messages      | Messages API      | Anthropic Messages API   | claude
//	openai-chatcompletions  | Chat Completions API | OpenAI Chat Completions API | openai
//	openai-embeddings       | Embeddings API    | OpenAI Embeddings API    | embeddings
//	openai-responses        | Responses API     | OpenAI Responses API     | responses
//	gemini-generatecontent  | GenerateContent API | Gemini GenerateContent API | gemini
//	gemini-interactions     | Interactions API  | Gemini Interactions API  | interactions
//	bedrock-converse        | Converse API      | Bedrock Converse API     | bedrock
//	azure-modelinference    | Model Inference API | Azure Model Inference API | azure
//
// gemini-interactions, bedrock-converse, and azure-modelinference are
// declared only (ParseProtocol/Name/FullName recognize them, provider
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
//	  - Claude            → anthropic-messages       (rawPredict; model in path)
//	  - some 3rd-party    → openai-chatcompletions   (/endpoints/openapi; partial coverage)
//	  - other 3rd-party   → publisher-native via rawPredict (no unified layer)
//
// anthropic-messages is the common denominator: Claude on all three clouds
// accepts the anthropic Messages body — only the transport differs.
type Protocol string

const (
	ProtocolAnthropicMessages     Protocol = "anthropic-messages"
	ProtocolOpenAIChatCompletions Protocol = "openai-chatcompletions"
	// ProtocolOpenAIEmbeddings is split out of the old openai-compatible
	// family; not exposed as a selectable protocol yet.
	ProtocolOpenAIEmbeddings      Protocol = "openai-embeddings"
	ProtocolOpenAIResponses       Protocol = "openai-responses"
	ProtocolGeminiGenerateContent Protocol = "gemini-generatecontent"
	// Transport-specific or not-yet-implemented protocols; no codec is
	// registered for these yet — they exist so provider definitions can
	// declare them as defaults, and ParseProtocol/Name/FullName recognize them.
	ProtocolGeminiInteractions  Protocol = "gemini-interactions"
	ProtocolBedrockConverse     Protocol = "bedrock-converse"
	ProtocolAzureModelInference Protocol = "azure-modelinference"
)

// String returns the canonical kebab-case identifier.
func (p Protocol) String() string { return string(p) }

// Name returns the short, vendor-agnostic display label (e.g. "Messages API").
// Use this where the surrounding UI already establishes the vendor (a
// provider's own card/section); use FullName where the protocol appears
// without that context.
func (p Protocol) Name() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "Messages API"
	case ProtocolOpenAIChatCompletions:
		return "Chat Completions API"
	case ProtocolOpenAIEmbeddings:
		return "Embeddings API"
	case ProtocolOpenAIResponses:
		return "Responses API"
	case ProtocolGeminiGenerateContent:
		return "GenerateContent API"
	case ProtocolGeminiInteractions:
		return "Interactions API"
	case ProtocolBedrockConverse:
		return "Converse API"
	case ProtocolAzureModelInference:
		return "Model Inference API"
	}
	return "Unknown"
}

// FullName returns the vendor-qualified display label (e.g. "Anthropic
// Messages API").
func (p Protocol) FullName() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "Anthropic Messages API"
	case ProtocolOpenAIChatCompletions:
		return "OpenAI Chat Completions API"
	case ProtocolOpenAIEmbeddings:
		return "OpenAI Embeddings API"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses API"
	case ProtocolGeminiGenerateContent:
		return "Gemini GenerateContent API"
	case ProtocolGeminiInteractions:
		return "Gemini Interactions API"
	case ProtocolBedrockConverse:
		return "Bedrock Converse API"
	case ProtocolAzureModelInference:
		return "Azure Model Inference API"
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
	case "openai-embeddings", "embeddings":
		return ProtocolOpenAIEmbeddings, nil
	case "openai-responses", "responses":
		return ProtocolOpenAIResponses, nil
	case "gemini-generatecontent", "gemini":
		return ProtocolGeminiGenerateContent, nil
	case "gemini-interactions", "interactions":
		return ProtocolGeminiInteractions, nil
	case "bedrock-converse", "bedrock":
		return ProtocolBedrockConverse, nil
	case "azure-modelinference", "azure":
		return ProtocolAzureModelInference, nil
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
