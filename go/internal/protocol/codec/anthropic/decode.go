package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestDecoder implements codec.RequestDecoder for Anthropic messages.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*ir.AiRequest, error) {
	var w request
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := ir.NewAiRequest(w.Model, nil)
	req.Meta.SourceProtocol = &ids.AnthropicMessages20230601
	req.System = decodeSystem(w.System)

	for _, m := range w.Messages {
		req.Messages = append(req.Messages, decodeMessage(m)...)
	}

	req.Generation = ir.GenerationConfig{
		MaxTokens:   &w.MaxTokens,
		Temperature: w.Temperature,
		TopP:        w.TopP,
		Stop:        w.Stop,
	}
	req.Stream.Enabled = w.Stream

	for _, t := range w.Tools {
		if t.Name != "" {
			req.Tools = append(req.Tools, ir.ToolSpec{
				Name: t.Name, Description: t.Description, Parameters: t.InputSchema,
			})
		}
	}
	if len(w.ToolChoice) > 0 {
		req.ToolChoice = decodeToolChoice(w.ToolChoice)
	}
	if w.Thinking != nil {
		req.Reasoning.Enabled = w.Thinking.Type == "enabled"
		req.Reasoning.BudgetTokens = w.Thinking.BudgetTokens
	}
	ext := &ir.AnthropicExt{
		TopK:              w.TopK,
		Container:         w.Container,
		ServiceTier:       w.ServiceTier,
		InferenceGeo:      w.InferenceGeo,
		Metadata:          w.Metadata,
		ContextManagement: w.ContextManagement,
	}
	if ext.TopK != nil || len(ext.Container) > 0 || ext.ServiceTier != "" ||
		ext.InferenceGeo != "" || len(ext.Metadata) > 0 || len(ext.ContextManagement) > 0 {
		req.Ext = ext
	}
	return req, nil
}

// decodeSystem parses the system field (string or []block) to text.
func decodeSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []systemBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" || b.Type == "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func decodeRole(s string) ir.Role {
	switch s {
	case "assistant":
		return ir.RoleAssistant
	case "system":
		return ir.RoleSystem
	default:
		return ir.RoleUser
	}
}

// decodeMessage turns an Anthropic message into one or more IR messages. A user
// turn containing tool_result blocks is split: each tool_result becomes a tool
// message, the rest become a user message.
func decodeMessage(m message) []ir.Message {
	role := decodeRole(m.Role)

	// Plain-string content.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []ir.Message{{Role: role, Content: &ir.TextContent{Text: s}}}
	}

	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return []ir.Message{{Role: role, Content: &ir.TextContent{}}}
	}

	var userBlocks []ir.ContentBlock
	var toolCalls []ir.ToolCall
	var rest []ir.Message // tool-result messages
	for _, b := range blocks {
		switch b.Type {
		case "text":
			userBlocks = append(userBlocks, &ir.TextBlock{Text: b.Text, CacheControl: decodeCacheControl(b.CacheControl)})
		case "thinking":
			userBlocks = append(userBlocks, &ir.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature, CacheControl: decodeCacheControl(b.CacheControl)})
		case "tool_use":
			toolCalls = append(toolCalls, ir.ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
			userBlocks = append(userBlocks, &ir.ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input, CacheControl: decodeCacheControl(b.CacheControl)})
		case "tool_result":
			if role == ir.RoleUser {
				rest = append(rest, ir.Message{
					Role:       ir.RoleTool,
					ToolCallID: b.ToolUseID,
					Content:    &ir.TextContent{Text: toolResultText(b.Content)},
				})
			} else {
				userBlocks = append(userBlocks, &ir.ToolResultBlock{ToolUseID: b.ToolUseID, Content: b.Content})
			}
		case "image":
			userBlocks = append(userBlocks, decodeImageSource(b.Source))
		}
	}

	if len(userBlocks) == 0 {
		return rest
	}
	msg := ir.Message{Role: role, Content: blockContent(userBlocks)}
	msg.ToolCalls = toolCalls
	// Primary message first, then any tool-result messages (mirrors Rust ordering).
	return append([]ir.Message{msg}, rest...)
}

// blockContent collapses blocks to TextContent when it is a single text block.
func blockContent(blocks []ir.ContentBlock) ir.MessageContent {
	if len(blocks) == 1 {
		if t, ok := blocks[0].(*ir.TextBlock); ok {
			return &ir.TextContent{Text: t.Text}
		}
	}
	return &ir.BlocksContent{Blocks: blocks}
}

// decodeImageSource parses an Anthropic image source (base64 or url) into an IR
// ImageBlock.
func decodeImageSource(raw json.RawMessage) ir.ContentBlock {
	if len(raw) == 0 {
		return &ir.UnknownBlock{}
	}
	var src struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(raw, &src) != nil {
		return &ir.UnknownBlock{Raw: raw}
	}
	switch src.Type {
	case "base64":
		return &ir.ImageBlock{Source: &ir.Base64Media{MediaType: src.MediaType, Data: src.Data}}
	case "url":
		return &ir.ImageBlock{Source: &ir.URLMedia{URL: src.URL}}
	}
	return &ir.UnknownBlock{Raw: raw}
}

// decodeCacheControl maps the Anthropic cache_control wire object to the IR
// CacheControl (Anthropic only supports ephemeral TTL: 5m or 1h).
func decodeCacheControl(raw json.RawMessage) *ir.CacheControl {
	if len(raw) == 0 {
		return nil
	}
	var cc struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &cc); err != nil {
		return nil
	}
	if cc.TTL == "1h" {
		return &ir.CacheControl{Ttl: ir.CacheTtlEphemeral1h}
	}
	return &ir.CacheControl{Ttl: ir.CacheTtlEphemeral5m}
}

// toolResultText extracts text from a tool_result content field.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw) // fallback: raw JSON text
}

func decodeToolChoice(raw json.RawMessage) ir.ToolChoice {
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &ir.RawToolChoice{Raw: raw}
	}
	switch obj.Type {
	case "auto":
		return &ir.AutoToolChoice{}
	case "none":
		return &ir.NoneToolChoice{}
	case "any":
		return &ir.RequiredToolChoice{}
	case "tool":
		if obj.Name != "" {
			return &ir.NamedToolChoice{Name: obj.Name}
		}
	}
	return &ir.RawToolChoice{Raw: raw}
}
