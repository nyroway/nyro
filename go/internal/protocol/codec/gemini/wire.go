// Package gemini implements the Google Generative AI (Gemini) codec:
// /v1beta/models/{model}:generateContent and :streamGenerateContent.
//
// Ported (scoped to the main paths) from
// crates/nyro-core/src/protocol/codec/google/gemini/. Unlike OpenAI/Anthropic,
// Gemini embeds the model in the URL path and uses a contents/parts body.
// P2 covers text, thought (thinking), functionCall/functionResponse, usage,
// and streaming. Rarer parts (fileData, executableCode, built-in tools,
// response_schema uppercasing) are deferred to P5 parity hardening.
package gemini

import "encoding/json"

// ── Request ──────────────────────────────────────────────────────────────────

type request struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	Tools             []toolEntry       `json:"tools,omitempty"`
	ToolConfig        json.RawMessage   `json:"toolConfig,omitempty"`
	SafetySettings    []safetySetting   `json:"safetySettings,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"` // "user" | "model"
	Parts []part `json:"parts"`
}

// part is a union keyed by which field is populated. Thought+Text marks a
// Gemini 2.5 thinking part.
type part struct {
	Text             string        `json:"text,omitempty"`
	Thought          bool          `json:"thought,omitempty"`
	ThoughtSignature string        `json:"thoughtSignature,omitempty"` // Gemini 3 reasoning token, sibling of functionCall
	InlineData       *inlineData   `json:"inlineData,omitempty"`
	FunctionCall     *functionCall `json:"functionCall,omitempty"`
	FunctionResponse *functionResp `json:"functionResponse,omitempty"`
}

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResp struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type generationConfig struct {
	Temperature        *float64        `json:"temperature,omitempty"`
	MaxOutputTokens    *uint32         `json:"maxOutputTokens,omitempty"`
	TopP               *float64        `json:"topP,omitempty"`
	TopK               *uint32         `json:"topK,omitempty"`
	CandidateCount     *uint32         `json:"candidateCount,omitempty"`
	StopSequences      []string        `json:"stopSequences,omitempty"`
	Seed               *uint32         `json:"seed,omitempty"`
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`
	ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
	ResponseLogprobs   *bool           `json:"responseLogprobs,omitempty"`
	Logprobs           *uint32         `json:"logprobs,omitempty"`
	ThinkingConfig     json.RawMessage `json:"thinkingConfig,omitempty"`
	ResponseModalities []string        `json:"responseModalities,omitempty"`
	ImageConfig        json.RawMessage `json:"imageConfig,omitempty"`
}

type toolEntry struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations,omitempty"`
}

type functionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type safetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// ── Response / stream chunk (same shape) ──────────────────────────────────────

type response struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     uint32 `json:"promptTokenCount"`
	CandidatesTokenCount uint32 `json:"candidatesTokenCount"`
	TotalTokenCount      uint32 `json:"totalTokenCount"`
}
