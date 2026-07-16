package responses

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

type responseDecoder struct{}

func (responseDecoder) Parse(body []byte) (*ir.AiResponse, error) {
	var w response
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	resp := ir.NewAiResponse(w.ID, w.Model)
	for _, it := range w.Output {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				resp.Content += c.Text
			}
		case "reasoning":
			// Reasoning items carry the chain-of-thought in content[].text
			// (type reasoning_text). Without this the thinking is dropped and
			// downstream clients see none. (OpenAI's encrypted summary is not
			// surfaced here.)
			for _, c := range it.Content {
				resp.ReasoningContent += c.Text
			}
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			resp.ToolCalls = append(resp.ToolCalls, ir.ToolCall{ID: it.CallID, Name: it.Name, Arguments: args})
		}
	}
	resp.StopReason = responsesStopReason(w.Status, len(resp.ToolCalls) > 0)
	if w.Usage != nil {
		resp.Usage = ir.Usage{
			PromptTokens:     w.Usage.InputTokens,
			CompletionTokens: w.Usage.OutputTokens,
			TotalTokens:      w.Usage.TotalTokens,
		}
	}
	return resp, nil
}

// responsesWireStatus maps the canonical IR stop reason back to the Responses
// API lifecycle status: length/content_filter mean the output was cut short
// (incomplete); everything else (stop/tool_calls/empty) completed.
func responsesWireStatus(stopReason string) string {
	switch stopReason {
	case "length", "max_tokens", "content_filter":
		return "incomplete"
	default:
		return "completed"
	}
}

// responsesStopReason maps the Responses API status to the canonical IR stop
// reason. The raw status ("completed"/"incomplete") is not valid downstream
// vocabulary (Anthropic/OpenAI), and a completed response carrying a
// function_call must become tool_calls so clients run the tool rather than
// ending the turn.
func responsesStopReason(status string, hasTool bool) string {
	switch status {
	case "completed":
		if hasTool {
			return "tool_calls"
		}
		return "stop"
	case "incomplete":
		return "length" // Responses incomplete is almost always max_output_tokens
	default:
		return status
	}
}

type responseEncoder struct{}

func (responseEncoder) Format(resp *ir.AiResponse) ([]byte, error) {
	var output []outputItem
	if resp.ReasoningContent != "" {
		output = append(output, outputItem{
			Type: "reasoning", ID: "rs", Status: "completed",
			Content: []outputContent{{Type: "reasoning_text", Text: resp.ReasoningContent}},
		})
	}
	if resp.Content != "" {
		output = append(output, outputItem{
			Type: "message", ID: "msg", Role: "assistant", Status: "completed",
			Content: []outputContent{{Type: "output_text", Text: resp.Content}},
		})
	}
	for _, tc := range resp.ToolCalls {
		args := tc.Arguments
		if args == "" {
			args = "{}"
		}
		output = append(output, outputItem{
			Type: "function_call", ID: "fc", CallID: tc.ID, Name: tc.Name,
			Arguments: args, Status: "completed",
		})
	}
	out := response{
		ID: resp.ID, Object: "response", Model: resp.Model,
		Output: output, Status: responsesWireStatus(resp.StopReason),
	}
	if resp.Usage.TotalTokens != 0 || resp.Usage.PromptTokens != 0 {
		out.Usage = &responseUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	return json.Marshal(out)
}

func nvl(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
