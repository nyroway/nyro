package ir

import (
	"encoding/json"
	"strings"
)

// MessageContent is either a plain string or a typed block list.
// Sealed union. Ported from MessageContent (untagged).
type MessageContent interface{ messageContent() }

// TextContent is a plain-string message body.
type TextContent struct{ Text string }

func (*TextContent) messageContent() {}

// BlocksContent is a list of typed content blocks.
type BlocksContent struct{ Blocks []ContentBlock }

func (*BlocksContent) messageContent() {}

// ToText flattens message content to a single string.
func ToText(c MessageContent) string {
	switch v := c.(type) {
	case *TextContent:
		return v.Text
	case *BlocksContent:
		var sb strings.Builder
		for _, b := range v.Blocks {
			if t, ok := AsText(b); ok {
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return ""
}

// Message is a single conversation turn.
type Message struct {
	Role       Role
	Content    MessageContent
	ToolCalls  []ToolCall      // optional (nil = absent)
	ToolCallID string          // optional; required for RoleTool
	Meta       json.RawMessage // optional provider-specific extras
}

// ToolCall is a model-issued tool call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}
