package ir

import "encoding/json"

// ResponseItem is a typed item in the response item graph (native for OpenAI
// Responses API, synthesized for other protocols). Sealed union.
// Ported from ResponseItem (serde tag = "type", rename_all = "snake_case").
type ResponseItem interface{ responseItem() }

// OutputTextItem is a text output block.
type OutputTextItem struct{ Text string }

func (*OutputTextItem) responseItem() {}

// ThinkingItem is a thinking / reasoning block.
type ThinkingItem struct{ Text string }

func (*ThinkingItem) responseItem() {}

// FunctionCallItem is a tool call issued by the model.
type FunctionCallItem struct {
	CallID    string
	Name      string
	Arguments string
}

func (*FunctionCallItem) responseItem() {}

// FunctionCallOutputItem is a tool result from the client (multi-turn Responses).
type FunctionCallOutputItem struct {
	CallID string
	Output string
}

func (*FunctionCallOutputItem) responseItem() {}

// WebSearchResultItem is a web-search result block (OpenAI built-in tool).
type WebSearchResultItem struct {
	URL     string
	Title   string
	Snippet string
}

func (*WebSearchResultItem) responseItem() {}

// UnknownItem is an unrecognized item type, preserved verbatim.
type UnknownItem struct{ Raw json.RawMessage }

func (*UnknownItem) responseItem() {}

// AiResponse is the unified egress IR produced by codec response parsers and
// the accumulator. Ported from AiResponse.
type AiResponse struct {
	ID                 string
	Model              string
	Content            string // primary text (convenience; also in items)
	ReasoningContent   string // optional
	ReasoningSignature string // optional (Anthropic multi-turn passback)
	ToolCalls          []ToolCall
	Items              []ResponseItem // optional (nil = absent)
	StopReason         string         // optional
	Usage              Usage
	Error              *AiError // optional
	Vendor             VendorExtensions
}

// NewAiResponse constructs an AiResponse with id and model.
func NewAiResponse(id, model string) *AiResponse {
	return &AiResponse{ID: id, Model: model}
}

// IsError reports whether the response carries a normalized error.
func (r *AiResponse) IsError() bool { return r.Error != nil }
