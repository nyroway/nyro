// Package spec defines the identity vocabulary of the llm protocol family:
// Protocol (a single concrete wire-format API surface) and ProtocolEndpoint
// (that protocol at a specific version).
//
// Canonical string form: "{protocol}/{version}" (e.g.
// "openai-chatcompletions/v1").
//
// spec has no dependencies beyond fmt, and that is deliberate: config,
// provider, admin and storage all need to name and parse protocols without
// being dragged into the codec interfaces. The rule for what belongs here is
// "anything a package that does not import codec still needs". Descriptor
// *types* live here; their *values* are declared by each codec leaf package.
package spec

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
// # Naming rule
//
//	Protocol ID  = {family}-{api}
//	package path = strings.ReplaceAll(id, "-", "/")
//
// family is the brand that owns the wire format's naming — openai, anthropic,
// gemini, and in future bedrock / azure. Note that family is not the vendor:
// Gemini is a protocol family with more than one API under it (generateContent
// and Interactions), and Bedrock Converse / Azure AI Model Inference are
// platform-branded rather than company-branded. api is the vendor's own API
// name, lowercased with all separators removed.
//
// There is exactly one "-", and the api segment must not contain another —
// the ID↔path bijection depends on it. Enforced by codec/layout_test.go.
//
// # Alias rule
//
// Each protocol has at most ONE alias: the single word users actually say,
// never a mechanical abbreviation of the full name. An alias is permanently
// bound to the protocol it was introduced for and does NOT follow the vendor's
// recommended default API — e.g. "gemini" stays on generateContent even after
// Google steers new projects to Interactions. New protocols get no alias by
// default.
//
// Identifier | Display Name | Alias:
//
//	anthropic-messages      | Anthropic Messages       | claude
//	openai-chatcompletions  | OpenAI Chat Completions  | openai
//	openai-responses        | OpenAI Responses         | codex
//	openai-embeddings       | OpenAI Embeddings        | embed
//	gemini-generatecontent  | Gemini generateContent   | gemini
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
//	  - Foundry non-Claude (Llama/Mistral)  → openai-chatcompletions (AI Model Inference API)
//
//	GCP Vertex AI (OAuth / service-account):
//	  - Gemini            → gemini-generatecontent
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
)

// String returns the canonical kebab-case identifier.
func (p Protocol) String() string { return string(p) }

// DisplayName returns the display label for a protocol (e.g. "Anthropic
// Messages"). Labels name the API, without an "API" suffix — the suffix was
// true of every entry and so distinguished none of them.
func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "Anthropic Messages"
	case ProtocolOpenAIChatCompletions:
		return "OpenAI Chat Completions"
	case ProtocolOpenAIEmbeddings:
		return "OpenAI Embeddings"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses"
	case ProtocolGeminiGenerateContent:
		return "Gemini generateContent"
	}
	return "Unknown"
}

// ParseProtocol resolves a canonical string or its single alias to a Protocol.
// See the alias rule on Protocol: one alias per protocol, permanently bound.
func ParseProtocol(s string) (Protocol, error) {
	switch s {
	case "anthropic-messages", "claude":
		return ProtocolAnthropicMessages, nil
	case "openai-chatcompletions", "openai":
		return ProtocolOpenAIChatCompletions, nil
	case "openai-embeddings", "embed":
		return ProtocolOpenAIEmbeddings, nil
	case "openai-responses", "codex":
		return ProtocolOpenAIResponses, nil
	case "gemini-generatecontent", "gemini":
		return ProtocolGeminiGenerateContent, nil
	}
	return "", fmt.Errorf("unknown protocol: %s", s)
}

// ProtocolEndpoint is a Protocol at a specific wire-format version.
//
// Canonical display: "{protocol}/{version}".
type ProtocolEndpoint struct {
	Protocol Protocol
	// Version is the version segment of the vendor's URL path — "v1" from
	// /v1/chat/completions, "v1beta" from /v1beta/models/{model}:...
	//
	// When a vendor's wire-format version lives on some other axis, that axis
	// belongs to the codec and the Authenticator, not here. Anthropic is the
	// case in point: its anthropic-version: 2023-06-01 header is written by
	// the codec and by provider.anthropicAuthenticator, while its endpoint is
	// anthropic-messages/v1 after the URL.
	//
	// Trade-off, so it does not read as an oversight: if Anthropic ever ships
	// an incompatible anthropic-version, two coexisting endpoints would both
	// carry URL segment v1 and Version could not tell them apart. Consistency
	// wins for now — /v1/ has not moved in three years and new capabilities
	// (thinking, cache_control) arrived additively under 2023-06-01. Revisit
	// the key if that day comes.
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
	AnthropicMessagesV1         = ProtocolEndpoint{ProtocolAnthropicMessages, "v1"}
	GeminiGenerateContentV1Beta = ProtocolEndpoint{ProtocolGeminiGenerateContent, "v1beta"}
)

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
		return AnthropicMessagesV1, true
	case ProtocolGeminiGenerateContent:
		return GeminiGenerateContentV1Beta, true
	}
	return ProtocolEndpoint{}, false
}
