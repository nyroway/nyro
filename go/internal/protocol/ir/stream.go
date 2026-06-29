package ir

// StreamDelta is a single parsed delta from a streaming response. The stream
// parser emits a sequence of StreamDelta values; the accumulator coalesces
// them into a complete AiResponse.
//
// Sealed union; dispatch via type switch. Ported from StreamDelta.
type StreamDelta interface{ streamDelta() }

// MessageStartDelta is the first chunk — identifies the response and model.
type MessageStartDelta struct {
	ID    string
	Model string
}

func (*MessageStartDelta) streamDelta() {}

// TextDelta is incremental text output.
type TextDelta struct{ Text string }

func (*TextDelta) streamDelta() {}

// ThinkingDelta is incremental thinking / reasoning output.
type ThinkingDelta struct{ Text string }

func (*ThinkingDelta) streamDelta() {}

// ThinkingSignatureDelta is a thinking signature for multi-turn passback.
type ThinkingSignatureDelta struct{ Signature string }

func (*ThinkingSignatureDelta) streamDelta() {}

// ToolCallStartDelta signals a tool call is starting.
type ToolCallStartDelta struct {
	Index uint
	ID    string
	Name  string
}

func (*ToolCallStartDelta) streamDelta() {}

// ToolCallDeltaDelta is an incremental tool-call argument JSON fragment.
type ToolCallDeltaDelta struct {
	Index     uint
	Arguments string
}

func (*ToolCallDeltaDelta) streamDelta() {}

// ToolCallCompleteDelta signals tool-call arguments are complete.
type ToolCallCompleteDelta struct {
	Index    uint
	ToolCall ToolCall
}

func (*ToolCallCompleteDelta) streamDelta() {}

// UsageDelta carries final token-usage statistics.
type UsageDelta struct{ Usage Usage }

func (*UsageDelta) streamDelta() {}

// DoneDelta signals the stream ended normally.
type DoneDelta struct{ StopReason string }

func (*DoneDelta) streamDelta() {}

// StreamErrorDelta is a mid-stream error detected by the parser.
type StreamErrorDelta struct{ Error *AiError }

func (*StreamErrorDelta) streamDelta() {}

// UnexpectedEOFDelta signals the stream was truncated without a [DONE] sentinel.
type UnexpectedEOFDelta struct{}

func (*UnexpectedEOFDelta) streamDelta() {}

// UnknownDelta is a verbatim SSE event not classified into any other variant.
type UnknownDelta struct{ Raw string }

func (*UnknownDelta) streamDelta() {}
