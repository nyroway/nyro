package responses

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
)

// requestEncoder implements codec.RequestEncoder for the Responses API.
type requestEncoder struct{}

// responsesMessageContent builds a Responses message content array from an IR
// message, preserving image blocks (input_image) alongside text. Using
// llm.ToText alone dropped images, so multimodal requests reached the model
// without the picture. Returns nil when there is nothing to send.
func responsesMessageContent(m llm.Message) json.RawMessage {
	textType := "input_text"
	if m.Role == llm.RoleAssistant {
		textType = "output_text"
	}
	var parts []map[string]string
	if bc, ok := m.Content.(*llm.BlocksContent); ok {
		for _, b := range bc.Blocks {
			switch blk := b.(type) {
			case *llm.TextBlock:
				if blk.Text != "" {
					parts = append(parts, map[string]string{"type": textType, "text": blk.Text})
				}
			case *llm.ImageBlock:
				if img, ok := blk.Source.(*llm.Base64Media); ok {
					parts = append(parts, map[string]string{"type": "input_image", "image_url": "data:" + img.MediaType + ";base64," + img.Data})
				} else if u, ok := blk.Source.(*llm.URLMedia); ok {
					parts = append(parts, map[string]string{"type": "input_image", "image_url": u.URL})
				}
			}
		}
	} else if t := llm.ToText(m.Content); t != "" {
		parts = append(parts, map[string]string{"type": textType, "text": t})
	}
	if len(parts) == 0 {
		return nil
	}
	b, _ := json.Marshal(parts)
	return b
}

func (requestEncoder) Encode(req *llm.ChatRequest) (codec.OutboundRequest, error) {
	var instructions []string
	var input []inputItem
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			if t := llm.ToText(m.Content); t != "" {
				instructions = append(instructions, t)
			}
		case llm.RoleUser, llm.RoleAssistant:
			if content := responsesMessageContent(m); content != nil {
				input = append(input, inputItem{Type: "message", Role: string(m.Role), Content: content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, inputItem{
					Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
		case llm.RoleTool:
			out, _ := json.Marshal(llm.ToText(m.Content))
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
		case *llm.AutoToolChoice:
			w.ToolChoice = json.RawMessage(`"auto"`)
		case *llm.NoneToolChoice:
			w.ToolChoice = json.RawMessage(`"none"`)
		case *llm.RequiredToolChoice:
			w.ToolChoice = json.RawMessage(`"required"`)
		case *llm.NamedToolChoice:
			b, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}})
			w.ToolChoice = b
		case *llm.RawToolChoice:
			w.ToolChoice = tc.Raw
		}
	}
	w.Store = false // privacy default — don't persist on OpenAI's side

	if req.Reasoning.Enabled && req.Reasoning.Effort != nil {
		r := map[string]string{}
		switch req.Reasoning.Effort.(type) {
		case *llm.ReasoningNone:
			r["effort"] = "none"
		case *llm.ReasoningMinimal:
			r["effort"] = "minimal"
		case *llm.ReasoningLow:
			r["effort"] = "low"
		case *llm.ReasoningMedium:
			r["effort"] = "medium"
		case *llm.ReasoningHigh:
			r["effort"] = "high"
		case *llm.ReasoningXhigh:
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
