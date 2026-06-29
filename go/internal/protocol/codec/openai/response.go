package openai

import (
	"encoding/json"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// responseDecoder implements codec.ResponseDecoder for non-streaming chat
// completions.
type responseDecoder struct{}

func (responseDecoder) Parse(body []byte) (*ir.AiResponse, error) {
	var w chatCompletion
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	resp := ir.NewAiResponse(w.ID, w.Model)
	if len(w.Choices) > 0 {
		c := w.Choices[0]
		resp.Content = textContent(c.Message.Content)
		if c.Message.ReasoningContent != "" {
			resp.ReasoningContent = c.Message.ReasoningContent
		} else if c.Message.Reasoning != "" {
			resp.ReasoningContent = c.Message.Reasoning
		}
		for _, tc := range c.Message.ToolCalls {
			resp.ToolCalls = append(resp.ToolCalls, ir.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		if c.FinishReason != nil {
			resp.StopReason = *c.FinishReason
		}
	}
	if w.Usage != nil {
		resp.Usage = usageFromWire(*w.Usage)
	}
	return resp, nil
}

// responseEncoder implements codec.ResponseEncoder for non-streaming chat
// completions.
type responseEncoder struct{}

func (responseEncoder) Format(resp *ir.AiResponse) ([]byte, error) {
	msg := chatMessage{Role: "assistant"}
	if resp.Content != "" {
		msg.Content = mustJSONString(resp.Content)
	}
	msg.ReasoningContent = resp.ReasoningContent
	for _, tc := range resp.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: chatToolCallFn{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	out := chatCompletion{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: nowUnix(),
		Choices: []chatChoice{{Index: 0, Message: msg}},
	}
	if resp.StopReason != "" {
		sr := toOpenAIFinishReason(resp.StopReason)
		out.Choices[0].FinishReason = &sr
	}
	if resp.Usage.PromptTokens != 0 || resp.Usage.CompletionTokens != 0 || resp.Usage.TotalTokens != 0 {
		u := usageToWire(resp.Usage)
		out.Usage = &u
	}
	return json.Marshal(out)
}

// textContent extracts the text from an OpenAI content field (string or parts).
func textContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return ir.ToText(decodeContent(raw))
}

func usageFromWire(u chatUsage) ir.Usage {
	result := ir.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	if u.PromptTokensDetails != nil {
		ct := u.PromptTokensDetails.CachedTokens
		result.CacheReadTokens = &ct
	}
	return result
}

func usageToWire(u ir.Usage) chatUsage {
	return chatUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}

// toOpenAIFinishReason maps a canonical IR stop_reason to OpenAI's
// finish_reason vocabulary (for cross-protocol Transform).
func toOpenAIFinishReason(s string) string {
	switch s {
	case "end_turn", "stop":
		return "stop"
	case "tool_use", "tool_calls":
		return "tool_calls"
	case "max_tokens", "length":
		return "length"
	default:
		return s
	}
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func nowUnix() int64 { return time.Now().Unix() }
