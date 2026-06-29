package anthropic

import (
	"encoding/json"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// responseDecoder parses a non-streaming Anthropic message response.
type responseDecoder struct{}

func (responseDecoder) Parse(body []byte) (*ir.AiResponse, error) {
	var w response
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	resp := ir.NewAiResponse(w.ID, w.Model)
	resp.StopReason = w.StopReason
	for _, b := range w.Content {
		switch b.Type {
		case "text":
			resp.Content += b.Text
		case "thinking":
			if resp.ReasoningContent != "" {
				resp.ReasoningContent += "\n"
			}
			resp.ReasoningContent += b.Thinking
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			resp.ToolCalls = append(resp.ToolCalls, ir.ToolCall{ID: b.ID, Name: b.Name, Arguments: args})
		}
	}
	resp.Usage = ir.Usage{
		PromptTokens:        w.Usage.InputTokens,
		CompletionTokens:    w.Usage.OutputTokens,
		TotalTokens:         w.Usage.InputTokens + w.Usage.OutputTokens,
		CacheReadTokens:     w.Usage.CacheReadTokens,
		CacheCreationTokens: w.Usage.CacheCreationTokens,
	}
	return resp, nil
}

// responseEncoder formats an IR response as an Anthropic message.
type responseEncoder struct{}

func (responseEncoder) Format(resp *ir.AiResponse) ([]byte, error) {
	var blocks []contentBlock
	if resp.ReasoningContent != "" {
		blocks = append(blocks, contentBlock{Type: "thinking", Thinking: resp.ReasoningContent})
	}
	if resp.Content != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: resp.Content})
	}
	for _, tc := range resp.ToolCalls {
		args := json.RawMessage(tc.Arguments)
		if tc.Arguments == "" {
			args = json.RawMessage("{}")
		}
		blocks = append(blocks, contentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: args})
	}
	out := response{
		ID: resp.ID, Type: "message", Role: "assistant", Model: resp.Model,
		Content: blocks, StopReason: toAnthropicStopReason(resp.StopReason),
		Usage: responseUsage{
			InputTokens:         resp.Usage.PromptTokens,
			OutputTokens:        resp.Usage.CompletionTokens,
			CacheReadTokens:     resp.Usage.CacheReadTokens,
			CacheCreationTokens: resp.Usage.CacheCreationTokens,
		},
	}
	return json.Marshal(out)
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// toAnthropicStopReason maps a canonical IR stop_reason to Anthropic's
// vocabulary (for cross-protocol Transform where the upstream speaks OpenAI).
func toAnthropicStopReason(s string) string {
	switch s {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return s // already Anthropic format or unknown
	}
}

func nowUnix() int64 { return time.Now().Unix() }
