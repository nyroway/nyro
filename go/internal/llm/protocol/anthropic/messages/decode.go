package messages

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

// requestDecoder implements protocol.RequestDecoder for Anthropic messages.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*llm.ChatRequest, error) {
	var w request
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := llm.NewChatRequest(w.Model, nil)
	req.System = decodeSystem(w.System)

	for _, m := range w.Messages {
		req.Messages = append(req.Messages, decodeMessage(m)...)
	}

	req.Generation = llm.GenerationConfig{
		MaxTokens:   &w.MaxTokens,
		Temperature: w.Temperature,
		TopP:        w.TopP,
		Stop:        w.Stop,
	}
	req.Stream.Enabled = w.Stream

	for _, t := range w.Tools {
		if t.Name != "" {
			req.Tools = append(req.Tools, llm.ToolSpec{
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
	ext := &llm.AnthropicExt{
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

func decodeRole(s string) llm.Role {
	switch s {
	case "assistant":
		return llm.RoleAssistant
	case "system":
		return llm.RoleSystem
	default:
		return llm.RoleUser
	}
}

// decodeMessage turns an Anthropic message into one or more IR messages. A user
// turn containing tool_result blocks is split: each tool_result becomes a tool
// message, the rest become a user message.
func decodeMessage(m message) []llm.Message {
	role := decodeRole(m.Role)

	// Plain-string content.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []llm.Message{{Role: role, Content: &llm.TextContent{Text: s}}}
	}

	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return []llm.Message{{Role: role, Content: &llm.TextContent{}}}
	}

	var userBlocks []llm.ContentBlock
	var toolCalls []llm.ToolCall
	var rest []llm.Message // tool-result messages
	for _, b := range blocks {
		switch b.Type {
		case "text":
			userBlocks = append(userBlocks, &llm.TextBlock{Text: b.Text, CacheControl: decodeCacheControl(b.CacheControl)})
		case "thinking":
			userBlocks = append(userBlocks, &llm.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature, CacheControl: decodeCacheControl(b.CacheControl)})
		case "tool_use":
			toolCalls = append(toolCalls, llm.ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
			userBlocks = append(userBlocks, &llm.ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input, CacheControl: decodeCacheControl(b.CacheControl)})
		case "tool_result":
			if role == llm.RoleUser {
				rest = append(rest, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: b.ToolUseID,
					Content:    &llm.TextContent{Text: toolResultText(b.Content)},
				})
			} else {
				userBlocks = append(userBlocks, &llm.ToolResultBlock{ToolUseID: b.ToolUseID, Content: b.Content})
			}
		case "image":
			userBlocks = append(userBlocks, decodeImageSource(b.Source))
		}
	}

	if len(userBlocks) == 0 {
		return rest
	}
	msg := llm.Message{Role: role, Content: blockContent(userBlocks)}
	msg.ToolCalls = toolCalls
	// Primary message first, then any tool-result messages (mirrors Rust ordering).
	return append([]llm.Message{msg}, rest...)
}

// blockContent collapses blocks to TextContent when it is a single text block.
func blockContent(blocks []llm.ContentBlock) llm.MessageContent {
	if len(blocks) == 1 {
		if t, ok := blocks[0].(*llm.TextBlock); ok {
			return &llm.TextContent{Text: t.Text}
		}
	}
	return &llm.BlocksContent{Blocks: blocks}
}

// decodeImageSource parses an Anthropic image source (base64 or url) into an IR
// ImageBlock.
func decodeImageSource(raw json.RawMessage) llm.ContentBlock {
	if len(raw) == 0 {
		return &llm.UnknownBlock{}
	}
	var src struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(raw, &src) != nil {
		return &llm.UnknownBlock{Raw: raw}
	}
	switch src.Type {
	case "base64":
		return &llm.ImageBlock{Source: &llm.Base64Media{MediaType: src.MediaType, Data: src.Data}}
	case "url":
		return &llm.ImageBlock{Source: &llm.URLMedia{URL: src.URL}}
	}
	return &llm.UnknownBlock{Raw: raw}
}

// decodeCacheControl maps the Anthropic cache_control wire object to the IR
// CacheControl (Anthropic only supports ephemeral TTL: 5m or 1h).
func decodeCacheControl(raw json.RawMessage) *llm.CacheControl {
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
		return &llm.CacheControl{Ttl: llm.CacheTtlEphemeral1h}
	}
	return &llm.CacheControl{Ttl: llm.CacheTtlEphemeral5m}
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

func decodeToolChoice(raw json.RawMessage) llm.ToolChoice {
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &llm.RawToolChoice{Raw: raw}
	}
	switch obj.Type {
	case "auto":
		return &llm.AutoToolChoice{}
	case "none":
		return &llm.NoneToolChoice{}
	case "any":
		return &llm.RequiredToolChoice{}
	case "tool":
		if obj.Name != "" {
			return &llm.NamedToolChoice{Name: obj.Name}
		}
	}
	return &llm.RawToolChoice{Raw: raw}
}
