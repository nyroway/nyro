// Package openai implements the OpenAI-compatible codec: chat completions
// (/v1/chat/completions) and embeddings (/v1/embeddings, later). It translates
// between the OpenAI Chat Completion wire format and the canonical IR.
//
// Ported from crates/nyro-core/src/protocol/codec/openai/compatible/. P1
// covers the chat-completions path end-to-end (text, tool calls, reasoning,
// usage, streaming). Rarer fields (logprobs bodies, audio, multimodal parts)
// are captured into OpenAIChatExt or deferred to P2.
package openai

import "encoding/json"

// ── Request ──────────────────────────────────────────────────────────────────

type chatRequest struct {
	Model                string             `json:"model"`
	Messages             []chatMessage      `json:"messages"`
	MaxTokens            *uint32            `json:"max_tokens,omitempty"`
	MaxCompletionTokens  *uint32            `json:"max_completion_tokens,omitempty"`
	Temperature          *float64           `json:"temperature,omitempty"`
	TopP                 *float64           `json:"top_p,omitempty"`
	N                    *uint32            `json:"n,omitempty"`
	Seed                 *int64             `json:"seed,omitempty"`
	Stop                 json.RawMessage    `json:"stop,omitempty"` // string or []string
	Stream               *bool              `json:"stream,omitempty"`
	StreamOptions        *streamOptions     `json:"stream_options,omitempty"`
	PresencePenalty      *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty     *float64           `json:"frequency_penalty,omitempty"`
	LogitBias            map[string]float64 `json:"logit_bias,omitempty"`
	Logprobs             *bool              `json:"logprobs,omitempty"`
	TopLogprobs          *uint32            `json:"top_logprobs,omitempty"`
	ResponseFormat       json.RawMessage    `json:"response_format,omitempty"`
	Tools                []chatTool         `json:"tools,omitempty"`
	ToolChoice           json.RawMessage    `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool              `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort      string             `json:"reasoning_effort,omitempty"` // legacy field
	Reasoning            json.RawMessage    `json:"reasoning,omitempty"`        // {effort,...}
	User                 string             `json:"user,omitempty"`
	ServiceTier          string             `json:"service_tier,omitempty"`
	Modalities           []string           `json:"modalities,omitempty"`
	Prediction           json.RawMessage    `json:"prediction,omitempty"`
	Audio                json.RawMessage    `json:"audio,omitempty"`
	WebSearchOptions     json.RawMessage    `json:"web_search_options,omitempty"`
	PromptCacheRetention string             `json:"prompt_cache_retention,omitempty"`
	Verbosity            string             `json:"verbosity,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ToolCalls        []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"` // "function"
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// chatToolCall is a fully-formed tool call (non-streaming messages/responses).
type chatToolCall struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"` // "function"
	Function chatToolCallFn `json:"function"`
}

type chatToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ── Non-streaming response ───────────────────────────────────────────────────

type chatCompletion struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"` // "chat.completion"
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        uint        `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

type chatUsage struct {
	PromptTokens        uint32 `json:"prompt_tokens"`
	CompletionTokens    uint32 `json:"completion_tokens"`
	TotalTokens         uint32 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens uint32 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// ── Streaming chunk ──────────────────────────────────────────────────────────

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"` // "chat.completion.chunk"
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        uint      `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason,omitempty"`
}

type chatDelta struct {
	Role             string              `json:"role,omitempty"`
	Content          string              `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	Reasoning        string              `json:"reasoning,omitempty"`
	ToolCalls        []chatDeltaToolCall `json:"tool_calls,omitempty"`
}

// chatDeltaToolCall is a streamed tool-call fragment, accumulated by index.
type chatDeltaToolCall struct {
	Index    uint           `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function chatToolCallFn `json:"function"`
}
