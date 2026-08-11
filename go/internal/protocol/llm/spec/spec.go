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

// ProtocolInfo is everything user-facing about one protocol.
type ProtocolInfo struct {
	ID Protocol `json:"id"`
	// DisplayName labels the API. No "API" suffix: it was true of every entry
	// and so distinguished none of them.
	DisplayName string `json:"displayName"`
	// Alias is the single frozen short name, or "" if the protocol has none.
	Alias string `json:"alias"`
	// DefaultBaseURL is the reference host for this API, offered as the
	// placeholder when configuring an upstream. It is a fact about the API
	// family, not a constraint: any provider speaking the wire format works.
	DefaultBaseURL string `json:"defaultBaseUrl"`
	// Selectable reports whether the protocol is offered in config and the
	// WebUI. A protocol can be fully implemented and still not selectable.
	Selectable bool `json:"selectable"`
}

// catalog is the single Go-side source for protocol metadata: DisplayName and
// ParseProtocol both read it, so there is one list to edit rather than three
// parallel switches that can disagree.
//
// internal/protocol/llm/spec/protocols.json mirrors this list and is asserted against it by
// contract_test.go, while the WebUI asserts its own table against the same
// file. That keeps the two hand-maintained copies honest without a codegen
// step or a runtime dependency between them.
var catalog = []ProtocolInfo{
	{ProtocolAnthropicMessages, "Anthropic Messages", "claude", "https://api.anthropic.com", true},
	{ProtocolGeminiGenerateContent, "Gemini generateContent", "gemini", "https://generativelanguage.googleapis.com", true},
	{ProtocolOpenAIChatCompletions, "OpenAI Chat Completions", "openai", "https://api.openai.com/v1", true},
	{ProtocolOpenAIEmbeddings, "OpenAI Embeddings", "embed", "https://api.openai.com/v1", false},
	{ProtocolOpenAIResponses, "OpenAI Responses", "codex", "https://api.openai.com/v1", true},
}

// Protocols returns the full catalog, ordered by identifier.
func Protocols() []ProtocolInfo {
	out := make([]ProtocolInfo, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the catalog entry for a protocol.
func Lookup(p Protocol) (ProtocolInfo, bool) {
	for _, info := range catalog {
		if info.ID == p {
			return info, true
		}
	}
	return ProtocolInfo{}, false
}

// DisplayName returns the display label for a protocol (e.g. "Anthropic
// Messages"), or "Unknown" for an unrecognised one.
func (p Protocol) DisplayName() string {
	if info, ok := Lookup(p); ok {
		return info.DisplayName
	}
	return "Unknown"
}

// ParseProtocol resolves a canonical identifier or its single alias to a
// Protocol. See the alias rule on Protocol: one alias per protocol, bound for
// good.
func ParseProtocol(s string) (Protocol, error) {
	for _, info := range catalog {
		if s == string(info.ID) || (info.Alias != "" && s == info.Alias) {
			return info.ID, nil
		}
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

// IngressRoute is one HTTP route a codec claims on the client-facing side.
//
// Pattern is a chi pattern, so it may contain path parameters — Gemini claims
// /v1beta/models/{resource} and splits {resource} into model:action itself.
type IngressRoute struct {
	Method  string
	Pattern string
}

// EndpointCapabilities is the static description of one ProtocolEndpoint:
// what it can do and how the gateway should wire it up.
//
// Values are declared by each codec leaf package, not here — spec owns the
// type so that packages which never import codec can still refer to it. Only
// fields with a live consumer belong in this struct; a descriptor nobody reads
// is worse than no descriptor at all. Add to it when something needs to branch
// on it, not in anticipation.
type EndpointCapabilities struct {
	// IngressRoutes are the client-facing routes this codec claims.
	// Consumer: Gateway route assembly, which walks codec.All().
	IngressRoutes []IngressRoute
	// Streaming reports whether the endpoint produces incremental responses.
	// Consumer: the dispatcher, to decide whether an upstream body is streamed
	// through the IR or copied verbatim.
	Streaming bool
}

// Resolving a Protocol to its ProtocolEndpoint is codec.EndpointFor: it reads
// the registry, so it needs no per-protocol switch here.
