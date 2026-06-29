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
	resp.StopReason = w.Status
	for _, it := range w.Output {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				resp.Content += c.Text
			}
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			resp.ToolCalls = append(resp.ToolCalls, ir.ToolCall{ID: it.CallID, Name: it.Name, Arguments: args})
		}
	}
	if w.Usage != nil {
		resp.Usage = ir.Usage{
			PromptTokens:     w.Usage.InputTokens,
			CompletionTokens: w.Usage.OutputTokens,
			TotalTokens:      w.Usage.TotalTokens,
		}
	}
	return resp, nil
}

type responseEncoder struct{}

func (responseEncoder) Format(resp *ir.AiResponse) ([]byte, error) {
	var output []outputItem
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
		Output: output, Status: nvl(resp.StopReason, "completed"),
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
