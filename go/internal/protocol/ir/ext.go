package ir

import "encoding/json"

// ProtocolExt carries fields specific to one protocol family that influence
// codec behaviour but are not consumed by gateway infrastructure (router,
// quota, cache-key, dispatcher). Sealed union.
//
// Populated by the ingress decoder; consumed by the egress encoder. When Nyro
// translates between protocols, the source ProtocolExt is discarded and the
// encoder relies solely on the core AiRequest fields.
// Ported from protocol/ir/ext.rs.
type ProtocolExt interface{ protocolExt() }

// OpenAIChatExt holds OpenAI Chat Completions extension fields.
type OpenAIChatExt struct {
	Audio                json.RawMessage // optional (audio.voice, audio.format)
	LogitBias            map[string]float64
	Logprobs             *bool
	TopLogprobs          *uint32
	Modalities           []string
	N                    *uint32
	Prediction           json.RawMessage
	PromptCacheRetention string // optional ("in_memory" | "24h")
	StreamOptions        json.RawMessage
	Verbosity            string // optional ("low" | "medium" | "high")
	WebSearchOptions     json.RawMessage
	ServiceTier          string
}

func (*OpenAIChatExt) protocolExt() {}

// OpenAIResponsesExt holds OpenAI Responses API extension fields.
type OpenAIResponsesExt struct {
	Background           *bool
	ContextManagement    json.RawMessage
	Conversation         json.RawMessage
	Include              []string
	PreviousResponseID   string
	Prompt               json.RawMessage
	PromptCacheRetention string
	StreamOptions        json.RawMessage
	TopLogprobs          *uint32
	Truncation           string // optional ("auto" | "disabled")
	ToolChoiceExt        json.RawMessage
}

func (*OpenAIResponsesExt) protocolExt() {}

// AnthropicExt holds Anthropic Messages extension fields.
type AnthropicExt struct {
	TopK              *uint32
	Container         json.RawMessage
	InferenceGeo      string
	OutputConfig      json.RawMessage
	ServiceTier       string
	ServerTools       []json.RawMessage
	Metadata          json.RawMessage
	ContextManagement json.RawMessage
}

func (*AnthropicExt) protocolExt() {}

// GoogleExt holds Google GenAI (Gemini) extension fields.
type GoogleExt struct {
	TopK               *uint32
	CandidateCount     *uint32
	ResponseLogprobs   *bool
	Logprobs           *uint32
	ResponseMimeType   string
	ResponseJSONSchema json.RawMessage
	ToolConfig         json.RawMessage
	CachedContent      string
	ResponseModalities []string
	ThinkingConfig     json.RawMessage
	ImageConfig        json.RawMessage
}

func (*GoogleExt) protocolExt() {}
