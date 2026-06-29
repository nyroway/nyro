package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestEncoder implements codec.RequestEncoder for Anthropic messages.
type requestEncoder struct{}

func (requestEncoder) Encode(req *ir.AiRequest) (codec.OutboundRequest, error) {
	maxTokens := uint32(4096) // Anthropic requires max_tokens
	if req.Generation.MaxTokens != nil {
		maxTokens = *req.Generation.MaxTokens
	}

	w := request{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Stream:      req.Stream.Enabled,
		Temperature: req.Generation.Temperature,
		TopP:        req.Generation.TopP,
		Stop:        req.Generation.Stop,
	}
	if req.System != "" {
		w.System = mustJSONString(req.System)
	}

	for _, m := range mergeConsecutiveSameRole(req.Messages) {
		w.Messages = append(w.Messages, encodeMessage(m))
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, toolDef{
			Name: t.Name, Description: t.Description, InputSchema: t.Parameters,
		})
	}
	if req.ToolChoice != nil {
		w.ToolChoice = encodeToolChoice(req.ToolChoice)
	}
	if req.Reasoning.Enabled {
		w.Thinking = &thinkingConfig{Type: "enabled", BudgetTokens: req.Reasoning.BudgetTokens}
	}
	if e, ok := req.Ext.(*ir.AnthropicExt); ok {
		w.TopK = e.TopK
		w.Container = e.Container
		w.ServiceTier = e.ServiceTier
		w.InferenceGeo = e.InferenceGeo
		w.Metadata = e.Metadata
		w.ContextManagement = e.ContextManagement
	}

	body, err := json.Marshal(w)
	if err != nil {
		return codec.OutboundRequest{}, err
	}
	return codec.OutboundRequest{
		Method: "POST",
		Path:   "/v1/messages",
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"anthropic-version": "2023-06-01",
		},
		Body:   body,
		Stream: req.Stream.Enabled,
	}, nil
}

func encodeMessage(m ir.Message) message {
	if m.Role == ir.RoleTool {
		// Tool result → user message with a tool_result block.
		toolUseID := normalizeToolID(m.ToolCallID)
		blocks := []contentBlock{{Type: "tool_result", ToolUseID: toolUseID, Content: textRawMsg(m)}}
		raw, _ := json.Marshal(blocks)
		return message{Role: "user", Content: raw}
	}
	role := "user"
	if m.Role == ir.RoleAssistant {
		role = "assistant"
	}
	return message{Role: role, Content: encodeContent(m)}
}

// encodeContent renders a message body to Anthropic content (string or blocks).
func encodeContent(m ir.Message) json.RawMessage {
	text, textOnly := textOf(m)
	if textOnly && len(m.ToolCalls) == 0 {
		b, _ := json.Marshal(text)
		return b
	}
	var blocks []contentBlock
	if bc, ok := m.Content.(*ir.BlocksContent); ok {
		for _, b := range bc.Blocks {
			blocks = append(blocks, encodeBlock(b))
		}
	} else if text != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: text})
	}
	// Only append from m.ToolCalls if the content blocks don't already carry
	// tool_use (prevents duplicates for Native Anthropic where the IR has both
	// ToolUseBlock in content AND ToolCalls on the message).
	hasToolUseInBlocks := false
	if bc, ok := m.Content.(*ir.BlocksContent); ok {
		for _, b := range bc.Blocks {
			if _, ok := b.(*ir.ToolUseBlock); ok {
				hasToolUseInBlocks = true
				break
			}
		}
	}
	if !hasToolUseInBlocks {
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Arguments)
			if tc.Arguments == "" {
				args = json.RawMessage("{}")
			}
			blocks = append(blocks, contentBlock{Type: "tool_use", ID: normalizeToolID(tc.ID), Name: tc.Name, Input: args})
		}
	}
	b, _ := json.Marshal(blocks)
	return b
}

func encodeBlock(b ir.ContentBlock) contentBlock {
	switch v := b.(type) {
	case *ir.TextBlock:
		cb := contentBlock{Type: "text", Text: v.Text}
		if v.CacheControl != nil {
			cb.CacheControl = encodeCacheControl(*v.CacheControl)
		}
		return cb
	case *ir.ThinkingBlock:
		cb := contentBlock{Type: "thinking", Thinking: v.Thinking}
		if v.Signature != "" {
			cb.Signature = v.Signature
		}
		if v.CacheControl != nil {
			cb.CacheControl = encodeCacheControl(*v.CacheControl)
		}
		return cb
	case *ir.ToolUseBlock:
		input := v.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		cb := contentBlock{Type: "tool_use", ID: v.ID, Name: v.Name, Input: input}
		if v.CacheControl != nil {
			cb.CacheControl = encodeCacheControl(*v.CacheControl)
		}
		return cb
	case *ir.ToolResultBlock:
		cb := contentBlock{Type: "tool_result", ToolUseID: v.ToolUseID, Content: v.Content}
		if v.CacheControl != nil {
			cb.CacheControl = encodeCacheControl(*v.CacheControl)
		}
		return cb
	case *ir.ImageBlock:
		return contentBlock{Type: "image", Source: encodeMediaSource(v.Source)}
	}
	return contentBlock{Type: "text"}
}

func encodeCacheControl(cc ir.CacheControl) json.RawMessage {
	if cc.Ttl == ir.CacheTtlEphemeral1h {
		return json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
	}
	return json.RawMessage(`{"type":"ephemeral"}`)
}

// normalizeToolID ensures a tool-call ID matches Anthropic's expected format:
// empty → "toolu_nyro"; already "toolu_"-prefixed → kept; otherwise sanitized
// and prefixed. DeepSeek and strict Anthropic endpoints reject non-toolu_ IDs.
func normalizeToolID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "toolu_nyro"
	}
	if strings.HasPrefix(id, "toolu_") {
		return id
	}
	var sb strings.Builder
	sb.WriteString("toolu_")
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

func encodeMediaSource(s ir.MediaSource) json.RawMessage {
	switch v := s.(type) {
	case *ir.Base64Media:
		b, _ := json.Marshal(map[string]string{"type": "base64", "media_type": v.MediaType, "data": v.Data})
		return b
	case *ir.URLMedia:
		b, _ := json.Marshal(map[string]string{"type": "url", "url": v.URL})
		return b
	}
	return nil
}

func encodeToolChoice(tc ir.ToolChoice) json.RawMessage {
	switch v := tc.(type) {
	case *ir.AutoToolChoice:
		return json.RawMessage(`{"type":"auto"}`)
	case *ir.NoneToolChoice:
		return json.RawMessage(`{"type":"none"}`)
	case *ir.RequiredToolChoice:
		return json.RawMessage(`{"type":"any"}`)
	case *ir.NamedToolChoice:
		b, _ := json.Marshal(map[string]string{"type": "tool", "name": v.Name})
		return b
	case *ir.RawToolChoice:
		return v.Raw
	}
	return json.RawMessage(`{"type":"auto"}`)
}

func textOf(m ir.Message) (string, bool) {
	if tc, ok := m.Content.(*ir.TextContent); ok {
		return tc.Text, true
	}
	return "", false
}

func textRawMsg(m ir.Message) json.RawMessage {
	if text, ok := textOf(m); ok {
		b, _ := json.Marshal(text)
		return b
	}
	return json.RawMessage(`""`)
}

// mergeConsecutiveSameRole merges consecutive messages with the same role
// (user+user, assistant+assistant) into a single message. Strict Anthropic
// endpoints (DeepSeek) reject consecutive same-role messages with 400.
// System messages are filtered (handled by the top-level system field).
func mergeConsecutiveSameRole(msgs []ir.Message) []ir.Message {
	var out []ir.Message
	for _, m := range msgs {
		if m.Role == ir.RoleSystem {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Role == m.Role {
			last := &out[len(out)-1]
			last.Content = mergeContent(last.Content, m.Content)
			last.ToolCalls = append(last.ToolCalls, m.ToolCalls...)
			if m.ToolCallID != "" {
				last.ToolCallID = m.ToolCallID
			}
		} else {
			out = append(out, m)
		}
	}
	return out
}

func mergeContent(a, b ir.MessageContent) ir.MessageContent {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if ta, ok := a.(*ir.TextContent); ok {
		if tb, ok := b.(*ir.TextContent); ok {
			return &ir.TextContent{Text: ta.Text + "\n" + tb.Text}
		}
	}
	return &ir.BlocksContent{Blocks: append(toBlocks(a), toBlocks(b)...)}
}

func toBlocks(c ir.MessageContent) []ir.ContentBlock {
	switch v := c.(type) {
	case *ir.BlocksContent:
		return v.Blocks
	case *ir.TextContent:
		if v.Text == "" {
			return nil
		}
		return []ir.ContentBlock{&ir.TextBlock{Text: v.Text}}
	}
	return nil
}
