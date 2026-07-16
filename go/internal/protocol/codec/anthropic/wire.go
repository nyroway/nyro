// Package anthropic implements the Anthropic Messages codec (/v1/messages).
//
// Ported (scoped to the main paths) from
// crates/nyro-core/src/protocol/codec/anthropic/messages/. P2 covers text,
// thinking, tool_use/tool_result, usage, and streaming. Advanced behaviour —
// cache_control raw round-trip, server/built-in tools, same-role message
// merging/reordering, tool-id normalization, payload validation, document and
// audio blocks — is deferred to P5 parity hardening (marked TODO).
package anthropic

import "encoding/json"

// ── Request ──────────────────────────────────────────────────────────────────

type request struct {
	Model             string          `json:"model"`
	Messages          []message       `json:"messages"`
	MaxTokens         uint32          `json:"max_tokens"`
	Stream            bool            `json:"stream,omitempty"`
	System            json.RawMessage `json:"system,omitempty"` // string OR []systemBlock
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	TopK              *uint32         `json:"top_k,omitempty"`
	Tools             []toolDef       `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	Thinking          *thinkingConfig `json:"thinking,omitempty"`
	Stop              []string        `json:"stop_sequences,omitempty"`
	Container         json.RawMessage `json:"container,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	InferenceGeo      string          `json:"inference_geo,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR []contentBlock
}

type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Type        string          `json:"type,omitempty"` // built-in tool type
}

type thinkingConfig struct {
	Type         string  `json:"type"` // "enabled" | "disabled"
	BudgetTokens *uint32 `json:"budget_tokens,omitempty"`
}

// contentBlock is a single Anthropic content block. Fields are a union keyed by
// Type; only the relevant fields are populated per type.
type contentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      json.RawMessage `json:"content,omitempty"` // tool_result content
	Source       json.RawMessage `json:"source,omitempty"`  // image/document source
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// ── Non-streaming response ───────────────────────────────────────────────────

type response struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"` // "message"
	Role       string         `json:"role"` // "assistant"
	Model      string         `json:"model"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage      responseUsage  `json:"usage"`
}

type responseUsage struct {
	InputTokens         uint32  `json:"input_tokens"`
	OutputTokens        uint32  `json:"output_tokens"`
	CacheReadTokens     *uint32 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens *uint32 `json:"cache_creation_input_tokens,omitempty"`
}

// ── Streaming events ─────────────────────────────────────────────────────────
//
// Anthropic SSE pairs an `event:` line with a `data:` line. The data payload
// always carries a `type` field matching the event name, so the stream decoder
// can dispatch on the parsed data alone (the proxy's SSE reader passes it the
// `data:` payload).

type streamEvent struct {
	Type string `json:"type"`
}

type messageStartPayload struct {
	Type    string `json:"type"` // "message_start"
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens         uint32  `json:"input_tokens"`
			CacheReadTokens     *uint32 `json:"cache_read_input_tokens,omitempty"`
			CacheCreationTokens *uint32 `json:"cache_creation_input_tokens,omitempty"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlockStartPayload struct {
	Type         string       `json:"type"` // "content_block_start"
	Index        uint         `json:"index"`
	ContentBlock contentBlock `json:"content_block"`
}

type contentBlockDeltaPayload struct {
	Type  string          `json:"type"` // "content_block_delta"
	Index uint            `json:"index"`
	Delta json.RawMessage `json:"delta"` // {type: text_delta|thinking_delta|input_json_delta|signature_delta, ...}
}

type contentBlockStopPayload struct {
	Type  string `json:"type"` // "content_block_stop"
	Index uint   `json:"index"`
}

type messageDeltaPayload struct {
	Type  string `json:"type"` // "message_delta"
	Delta struct {
		StopReason string `json:"stop_reason,omitempty"`
	} `json:"delta"`
	Usage *struct {
		// InputTokens (and the cache counters) are included in message_delta by
		// some Anthropic-compatible providers (e.g. Zhipu/GLM), which report the
		// full usage only in the final message_delta and leave message_start's
		// usage all-zero. Real Anthropic omits input_tokens here (it lives in
		// message_start), so an absent field decodes to 0 and the decoder keeps
		// the message_start value. See streamResponseDecoder.ParseChunk.
		InputTokens         uint32  `json:"input_tokens"`
		OutputTokens        uint32  `json:"output_tokens"`
		CacheReadTokens     *uint32 `json:"cache_read_input_tokens,omitempty"`
		CacheCreationTokens *uint32 `json:"cache_creation_input_tokens,omitempty"`
	} `json:"usage,omitempty"`
}

type messageStopPayload struct {
	Type string `json:"type"` // "message_stop"
}

// deltaPayload is the inner delta of a content_block_delta event.
type deltaPayload struct {
	Type        string `json:"type"` // text_delta | thinking_delta | input_json_delta | signature_delta
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
}
