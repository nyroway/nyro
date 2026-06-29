package responses

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestEncoder implements codec.RequestEncoder for the Responses API.
type requestEncoder struct{}

func (requestEncoder) Encode(req *ir.AiRequest) (codec.OutboundRequest, error) {
	var instructions []string
	var input []inputItem
	for _, m := range req.Messages {
		switch m.Role {
		case ir.RoleSystem:
			if t := ir.ToText(m.Content); t != "" {
				instructions = append(instructions, t)
			}
		case ir.RoleUser, ir.RoleAssistant:
			if text := ir.ToText(m.Content); text != "" {
				contentType := "input_text"
				if m.Role == ir.RoleAssistant {
					contentType = "output_text"
				}
				content, _ := json.Marshal([]map[string]string{{"type": contentType, "text": text}})
				input = append(input, inputItem{Type: "message", Role: string(m.Role), Content: content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, inputItem{
					Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
		case ir.RoleTool:
			out, _ := json.Marshal(ir.ToText(m.Content))
			input = append(input, inputItem{
				Type: "function_call_output", CallID: m.ToolCallID, Output: out,
			})
		}
	}
	if len(input) == 0 {
		return codec.OutboundRequest{}, errors.New("responses request requires at least one input item")
	}

	inst := "You are a helpful assistant."
	if len(instructions) > 0 {
		inst = strings.Join(instructions, "\n\n")
	}

	inputJSON, _ := json.Marshal(input)
	w := request{
		Model:             req.Model,
		Input:             inputJSON,
		Instructions:      inst,
		Stream:            req.Stream.Enabled,
		Temperature:       req.Generation.Temperature,
		MaxOutputTokens:   req.Generation.MaxTokens,
		TopP:              req.Generation.TopP,
		ParallelToolCalls: req.ParallelToolCalls,
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, toolDef{
			Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict,
		})
	}
	if req.ToolChoice != nil {
		switch tc := req.ToolChoice.(type) {
		case *ir.AutoToolChoice:
			w.ToolChoice = json.RawMessage(`"auto"`)
		case *ir.NoneToolChoice:
			w.ToolChoice = json.RawMessage(`"none"`)
		case *ir.RequiredToolChoice:
			w.ToolChoice = json.RawMessage(`"required"`)
		case *ir.NamedToolChoice:
			b, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}})
			w.ToolChoice = b
		case *ir.RawToolChoice:
			w.ToolChoice = tc.Raw
		}
	}
	w.Store = false // privacy default — don't persist on OpenAI's side

	if req.Reasoning.Enabled && req.Reasoning.Effort != nil {
		r := map[string]string{}
		switch req.Reasoning.Effort.(type) {
		case *ir.ReasoningNone:
			r["effort"] = "none"
		case *ir.ReasoningMinimal:
			r["effort"] = "minimal"
		case *ir.ReasoningLow:
			r["effort"] = "low"
		case *ir.ReasoningMedium:
			r["effort"] = "medium"
		case *ir.ReasoningHigh:
			r["effort"] = "high"
		case *ir.ReasoningXhigh:
			r["effort"] = "xhigh"
		}
		if req.Reasoning.Display != "" {
			r["summary"] = req.Reasoning.Display
		}
		b, _ := json.Marshal(r)
		w.Reasoning = b
	}

	body, err := json.Marshal(w)
	if err != nil {
		return codec.OutboundRequest{}, err
	}
	return codec.OutboundRequest{
		Method:  "POST",
		Path:    "/v1/responses",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Stream:  req.Stream.Enabled,
	}, nil
}
