package gemini

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// responseDecoder parses a non-streaming Gemini generateContent response.
type responseDecoder struct{}

func (responseDecoder) Parse(body []byte) (*ir.AiResponse, error) {
	var w response
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	resp := ir.NewAiResponse(w.ResponseID, w.ModelVersion)
	if len(w.Candidates) > 0 {
		c := w.Candidates[0]
		resp.StopReason = normalizeGeminiFinishReason(c.FinishReason)
		for _, p := range c.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				args := string(p.FunctionCall.Args)
				if args == "" {
					args = "{}"
				}
				resp.ToolCalls = append(resp.ToolCalls, ir.ToolCall{Name: p.FunctionCall.Name, Arguments: args})
			case p.Thought:
				resp.ReasoningContent += p.Text
			default:
				resp.Content += p.Text
			}
		}
		// Gemini signals tool use only via functionCall parts — its finishReason
		// stays STOP — so upgrade the canonical stop reason to "tool_calls" when
		// a tool call is present. Without this, downstream protocols emit
		// end_turn instead of tool_use and clients never run the tool. Only
		// override a normal stop (not length/content_filter, which mean the
		// tool call may be truncated).
		if len(resp.ToolCalls) > 0 && resp.StopReason == "stop" {
			resp.StopReason = "tool_calls"
		}
	}
	if w.UsageMetadata != nil {
		resp.Usage = ir.Usage{
			PromptTokens:     w.UsageMetadata.PromptTokenCount,
			CompletionTokens: w.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      w.UsageMetadata.TotalTokenCount,
		}
	}
	return resp, nil
}

// responseEncoder formats an IR response as a Gemini generateContent response.
type responseEncoder struct{}

func (responseEncoder) Format(resp *ir.AiResponse) ([]byte, error) {
	var parts []part
	if resp.ReasoningContent != "" {
		parts = append(parts, part{Text: resp.ReasoningContent, Thought: true})
	}
	if resp.Content != "" {
		parts = append(parts, part{Text: resp.Content})
	}
	for _, tc := range resp.ToolCalls {
		args := json.RawMessage(tc.Arguments)
		if tc.Arguments == "" {
			args = json.RawMessage("{}")
		}
		parts = append(parts, part{FunctionCall: &functionCall{Name: tc.Name, Args: args}})
	}
	out := response{
		Candidates: []candidate{{
			Content:      content{Role: "model", Parts: parts},
			FinishReason: denormalizeGeminiFinishReason(resp.StopReason),
		}},
	}
	if resp.Usage.TotalTokens != 0 || resp.Usage.PromptTokens != 0 {
		out.UsageMetadata = &usageMetadata{
			PromptTokenCount:     resp.Usage.PromptTokens,
			CandidatesTokenCount: resp.Usage.CompletionTokens,
			TotalTokenCount:      resp.Usage.TotalTokens,
		}
	}
	return json.Marshal(out)
}
