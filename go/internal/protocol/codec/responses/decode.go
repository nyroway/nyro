package responses

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*ir.AiRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	model := strField(obj, "model")
	if model == "" {
		return nil, errors.New("missing 'model'")
	}

	req := ir.NewAiRequest(model, nil)
	req.Meta.SourceProtocol = &ids.OpenAIResponsesV1
	req.Stream.Enabled = boolField(obj, "stream")
	req.Generation = ir.GenerationConfig{
		Temperature: floatField(obj, "temperature"),
		MaxTokens:   uintField(obj, "max_output_tokens"),
		TopP:        floatField(obj, "top_p"),
	}
	req.ParallelToolCalls = boolPtrField(obj, "parallel_tool_calls")

	if inst := strField(obj, "instructions"); inst != "" {
		req.Messages = append(req.Messages, ir.Message{Role: ir.RoleSystem, Content: &ir.TextContent{Text: inst}})
	}

	// input: string or []item
	inputRaw, ok := obj["input"]
	if !ok {
		return nil, errors.New("missing 'input'")
	}
	var inputStr string
	if json.Unmarshal(inputRaw, &inputStr) == nil {
		req.Messages = append(req.Messages, ir.Message{Role: ir.RoleUser, Content: &ir.TextContent{Text: inputStr}})
	} else {
		var items []inputItem
		if err := json.Unmarshal(inputRaw, &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			if m, ok := decodeInputItem(it); ok {
				req.Messages = append(req.Messages, m)
			}
		}
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("no messages found in input")
	}

	// tools
	if raw, ok := obj["tools"]; ok {
		var tools []toolDef
		if json.Unmarshal(raw, &tools) == nil {
			for _, t := range tools {
				if t.Type == "function" || t.Type == "" {
					req.Tools = append(req.Tools, ir.ToolSpec{
						Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict,
					})
				}
			}
		}
	}

	// reasoning
	if raw, ok := obj["reasoning"]; ok {
		var r struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal(raw, &r) == nil && (r.Effort != "" || r.Summary != "") {
			req.Reasoning.Enabled = true
			req.Reasoning.Effort = decodeEffort(r.Effort)
			req.Reasoning.Display = r.Summary
		}
	}
	return req, nil
}

// decodeInputItem maps a Responses input item to an IR message.
func decodeInputItem(it inputItem) (ir.Message, bool) {
	switch it.Type {
	case "message":
		return decodeMessageItem(it)
	case "function_call":
		args := it.Arguments
		if args == "" {
			args = "{}"
		}
		return ir.Message{
			Role:      ir.RoleAssistant,
			Content:   &ir.TextContent{},
			ToolCalls: []ir.ToolCall{{ID: it.CallID, Name: it.Name, Arguments: args}},
		}, it.CallID != "" && it.Name != ""
	case "function_call_output":
		return ir.Message{
			Role: ir.RoleTool, ToolCallID: it.CallID,
			Content: &ir.TextContent{Text: outputText(it.Output)},
		}, it.CallID != ""
	case "reasoning", "web_search_call", "file_search_call", "computer_call":
		return ir.Message{}, false // TODO(P5): reasoning passback / built-in calls
	}
	return ir.Message{}, false
}

// decodeMessageItem parses a message item's content (string or text blocks).
func decodeMessageItem(it inputItem) (ir.Message, bool) {
	role := ir.RoleUser
	switch it.Role {
	case "system", "developer":
		role = ir.RoleSystem
	case "assistant":
		role = ir.RoleAssistant
	}
	// content may be a string or an array of {type, text} blocks.
	var s string
	if json.Unmarshal(it.Content, &s) == nil {
		return ir.Message{Role: role, Content: &ir.TextContent{Text: s}}, s != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(it.Content, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "input_text" || b.Type == "output_text" || b.Type == "text" || b.Type == "" {
				texts = append(texts, b.Text)
			}
		}
		text := strings.Join(texts, "")
		if text == "" {
			return ir.Message{}, false
		}
		return ir.Message{Role: role, Content: &ir.TextContent{Text: text}}, true
	}
	return ir.Message{}, false
}

// outputText extracts text from a function_call_output's output field.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func decodeEffort(s string) ir.ReasoningEffort {
	switch s {
	case "low":
		return &ir.ReasoningLow{}
	case "high":
		return &ir.ReasoningHigh{}
	case "":
		return nil
	}
	return &ir.ReasoningMedium{}
}

// ── field helpers ──

func strField(obj map[string]json.RawMessage, key string) string {
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func boolField(obj map[string]json.RawMessage, key string) bool {
	raw, ok := obj[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

func boolPtrField(obj map[string]json.RawMessage, key string) *bool {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return &b
	}
	return nil
}

func floatField(obj map[string]json.RawMessage, key string) *float64 {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return &f
	}
	return nil
}

func uintField(obj map[string]json.RawMessage, key string) *uint32 {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var n uint32
	if json.Unmarshal(raw, &n) == nil {
		return &n
	}
	return nil
}
