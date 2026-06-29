package ir

import "encoding/json"

// ContentBlock is a typed block within a message's content list.
// Sealed union; dispatch via type switch. Ported from ContentBlock.
type ContentBlock interface{ contentBlock() }

// TextBlock is a text output/input block.
type TextBlock struct {
	Text         string
	CacheControl *CacheControl // optional
}

func (*TextBlock) contentBlock() {}

// ImageBlock is a multimodal image block.
type ImageBlock struct {
	Source       MediaSource
	CacheControl *CacheControl // optional
}

func (*ImageBlock) contentBlock() {}

// AudioBlock is a multimodal audio block.
type AudioBlock struct{ Source MediaSource }

func (*AudioBlock) contentBlock() {}

// FileBlock is a generic file block.
type FileBlock struct {
	Source    MediaSource
	MediaType string // optional
}

func (*FileBlock) contentBlock() {}

// ThinkingBlock is extended-thinking output (Anthropic thinking, Google thought).
type ThinkingBlock struct {
	Thinking     string
	Signature    string        // optional (Anthropic multi-turn passback)
	CacheControl *CacheControl // optional (Anthropic cache breakpoint)
}

func (*ThinkingBlock) contentBlock() {}

// RedactedThinkingBlock is a redacted thinking block (Anthropic).
type RedactedThinkingBlock struct{ Data string }

func (*RedactedThinkingBlock) contentBlock() {}

// ToolUseBlock is a tool call issued by the model.
type ToolUseBlock struct {
	ID           string
	Name         string
	Input        json.RawMessage
	CacheControl *CacheControl // optional
}

func (*ToolUseBlock) contentBlock() {}

// ToolResultBlock is a tool result provided by the client.
type ToolResultBlock struct {
	ToolUseID    string
	Content      json.RawMessage
	IsError      *bool // optional
	CacheControl *CacheControl
}

func (*ToolResultBlock) contentBlock() {}

// ServerToolUseBlock is a server-executed tool call (Anthropic, Google).
type ServerToolUseBlock struct {
	ID           string
	Name         string
	Input        json.RawMessage
	ServerType   string // optional discriminator (e.g. "web_search", "bash")
	CacheControl *CacheControl
}

func (*ServerToolUseBlock) contentBlock() {}

// ServerToolResultBlock is a result from a server-executed tool.
type ServerToolResultBlock struct {
	ToolUseID    string
	Content      json.RawMessage
	ServerType   string // optional
	CacheControl *CacheControl
}

func (*ServerToolResultBlock) contentBlock() {}

// DocumentBlock is a document block (Anthropic DocumentBlockParam).
type DocumentBlock struct {
	Source       DocumentSource
	Title        string // optional
	Context      string // optional
	CacheControl *CacheControl
}

func (*DocumentBlock) contentBlock() {}

// SearchResultBlock is a search-result block (Anthropic SearchResultBlockParam).
type SearchResultBlock struct {
	Content      []ContentBlock
	Source       string
	Title        string
	CacheControl *CacheControl
}

func (*SearchResultBlock) contentBlock() {}

// CitationBlock is a citation (Anthropic citations, OpenAI Responses annotations).
type CitationBlock struct {
	CitedText string
	Source    json.RawMessage
}

func (*CitationBlock) contentBlock() {}

// ExecutableCodeBlock is executable code produced by the model (Google).
type ExecutableCodeBlock struct {
	Code     string
	Language string // optional
	ID       string // optional
}

func (*ExecutableCodeBlock) contentBlock() {}

// CodeExecutionResultBlock is a code-execution result (Google, Anthropic).
type CodeExecutionResultBlock struct {
	ReturnCode int32
	Stdout     string
	Stderr     string
	ID         string // optional
}

func (*CodeExecutionResultBlock) contentBlock() {}

// ContainerUploadBlock is a container file upload (Anthropic).
type ContainerUploadBlock struct {
	FileID       string
	CacheControl *CacheControl
}

func (*ContainerUploadBlock) contentBlock() {}

// RefusalBlock is a model refusal (OpenAI content_filter / Anthropic refusal).
type RefusalBlock struct{ Refusal string }

func (*RefusalBlock) contentBlock() {}

// UnknownBlock is a raw JSON block the codec does not understand; preserved
// verbatim for pass-through and future extension.
type UnknownBlock struct{ Raw json.RawMessage }

func (*UnknownBlock) contentBlock() {}

// AsText returns the block's text if b is a *TextBlock.
func AsText(b ContentBlock) (string, bool) {
	if t, ok := b.(*TextBlock); ok {
		return t.Text, true
	}
	return "", false
}

// IsToolUse reports whether b is a (server or regular) tool-use block.
func IsToolUse(b ContentBlock) bool {
	switch b.(type) {
	case *ToolUseBlock, *ServerToolUseBlock:
		return true
	}
	return false
}

// IsToolResult reports whether b is a (server or regular) tool-result block.
func IsToolResult(b ContentBlock) bool {
	switch b.(type) {
	case *ToolResultBlock, *ServerToolResultBlock:
		return true
	}
	return false
}
