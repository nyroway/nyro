// Package responses implements the OpenAI Responses API codec (/v1/responses),
// which is item-graph based: input/output are arrays of typed items (message,
// function_call, function_call_output, reasoning) and streaming uses typed
// events (response.created, response.output_text.delta, response.completed...).
//
// Ported (scoped to the main paths) from
// crates/nyro-core/src/protocol/codec/openai/responses/. P2 covers text,
// function_call/function_call_output, usage, and the core streaming events.
// Reasoning-item passback, built-in tools, force_upstream_stream aggregation,
// and the full event sequence (output_item/content_part add/done) are deferred
// to P5 parity hardening.
package responses

import "encoding/json"

// ── Request ──────────────────────────────────────────────────────────────────

type request struct {
	Model             string          `json:"model"`
	Input             json.RawMessage `json:"input"` // string OR []inputItem
	Instructions      string          `json:"instructions,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	MaxOutputTokens   *uint32         `json:"max_output_tokens,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Tools             []toolDef       `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         json.RawMessage `json:"reasoning,omitempty"`
	Store             bool            `json:"store"`
}

type toolDef struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// inputItem is one element of the input array.
type inputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string OR []block (message)
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

// ── Response ─────────────────────────────────────────────────────────────────

type response struct {
	ID     string         `json:"id"`
	Object string         `json:"object"` // "response"
	Model  string         `json:"model"`
	Output []outputItem   `json:"output"`
	Status string         `json:"status,omitempty"`
	Usage  *responseUsage `json:"usage,omitempty"`
}

type outputItem struct {
	Type      string          `json:"type"` // message | function_call
	ID        string          `json:"id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   []outputContent `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Status    string          `json:"status,omitempty"`
}

type outputContent struct {
	Type string `json:"type"` // output_text
	Text string `json:"text"`
}

type responseUsage struct {
	InputTokens  uint32 `json:"input_tokens"`
	OutputTokens uint32 `json:"output_tokens"`
	TotalTokens  uint32 `json:"total_tokens"`
}

// ── Stream events ────────────────────────────────────────────────────────────

type streamEvent struct {
	Type     string       `json:"type"`
	Response *responseRef `json:"response,omitempty"`
	Delta    string       `json:"delta,omitempty"`
	Item     *outputItem  `json:"item,omitempty"`
}

// responseRef is the `response` sub-object on created/in_progress/completed.
type responseRef struct {
	ID     string         `json:"id,omitempty"`
	Model  string         `json:"model,omitempty"`
	Status string         `json:"status,omitempty"`
	Usage  *responseUsage `json:"usage,omitempty"`
}
