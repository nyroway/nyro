package gemini

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestEncoder implements codec.RequestEncoder for Gemini. The model lives in
// the egress URL path, not the body.
type requestEncoder struct{}

func (requestEncoder) Encode(req *ir.AiRequest) (codec.OutboundRequest, error) {
	w := request{}
	if req.System != "" {
		w.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	for _, m := range req.Messages {
		if m.Role == ir.RoleSystem {
			continue
		}
		w.Contents = append(w.Contents, encodeContent(m))
	}

	gc := &generationConfig{}
	if req.Generation.Temperature != nil {
		gc.Temperature = req.Generation.Temperature
	}
	if req.Generation.MaxTokens != nil {
		gc.MaxOutputTokens = req.Generation.MaxTokens
	}
	if req.Generation.TopP != nil {
		gc.TopP = req.Generation.TopP
	}
	if len(req.Generation.Stop) > 0 {
		gc.StopSequences = req.Generation.Stop
	}
	if req.Reasoning.Enabled && req.Reasoning.BudgetTokens != nil {
		b, _ := json.Marshal(map[string]uint32{"thinkingBudget": *req.Reasoning.BudgetTokens})
		gc.ThinkingConfig = b
	}
	if e, ok := req.Ext.(*ir.GoogleExt); ok {
		gc.TopK = e.TopK
		gc.CandidateCount = e.CandidateCount
		gc.ResponseLogprobs = e.ResponseLogprobs
		gc.Logprobs = e.Logprobs
		gc.ResponseMimeType = e.ResponseMimeType
		if len(e.ResponseJSONSchema) > 0 {
			gc.ResponseSchema = sanitizeGeminiSchema(e.ResponseJSONSchema)
		}
		if len(e.ThinkingConfig) > 0 {
			gc.ThinkingConfig = e.ThinkingConfig
		}
		if len(e.ResponseModalities) > 0 {
			gc.ResponseModalities = e.ResponseModalities
		}
		if len(e.ImageConfig) > 0 {
			gc.ImageConfig = e.ImageConfig
		}
		if len(e.ToolConfig) > 0 {
			w.ToolConfig = e.ToolConfig
		}
		if e.CachedContent != "" {
			w.CachedContent = e.CachedContent
		}
	}
	if gc.Temperature != nil || gc.MaxOutputTokens != nil || gc.TopP != nil || gc.TopK != nil ||
		len(gc.StopSequences) > 0 || len(gc.ThinkingConfig) > 0 || gc.ResponseMimeType != "" ||
		gc.CandidateCount != nil || len(gc.ResponseModalities) > 0 || len(gc.ImageConfig) > 0 {
		w.GenerationConfig = gc
	}

	var decls []functionDecl
	for _, t := range req.Tools {
		decls = append(decls, functionDecl{Name: t.Name, Description: t.Description, Parameters: sanitizeGeminiSchema(t.Parameters)})
	}
	if len(decls) > 0 {
		w.Tools = []toolEntry{{FunctionDeclarations: decls}}
	}
	for _, ss := range req.SafetySettings {
		w.SafetySettings = append(w.SafetySettings, safetySetting{Category: ss.Category, Threshold: ss.Threshold})
	}

	body, err := json.Marshal(w)
	if err != nil {
		return codec.OutboundRequest{}, err
	}
	path := "/v1beta/models/" + req.Model + ":generateContent"
	if req.Stream.Enabled {
		path = "/v1beta/models/" + req.Model + ":streamGenerateContent?alt=sse"
	}
	return codec.OutboundRequest{
		Method:  "POST",
		Path:    path,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Stream:  req.Stream.Enabled,
	}, nil
}

func encodeContent(m ir.Message) content {
	if m.Role == ir.RoleTool {
		// functionResponse (Gemini keys results by function name, not call id).
		return content{Role: "user", Parts: []part{{FunctionResponse: &functionResp{
			Name: m.ToolCallID, Response: geminiFunctionResponse(mustJSONRaw(ir.ToText(m.Content))),
		}}}}
	}
	role := "user"
	if m.Role == ir.RoleAssistant {
		role = "model"
	}
	var parts []part
	if bc, ok := m.Content.(*ir.BlocksContent); ok {
		for _, b := range bc.Blocks {
			parts = append(parts, encodeBlock(b))
		}
	} else if tc, ok := m.Content.(*ir.TextContent); ok && tc.Text != "" {
		parts = append(parts, part{Text: tc.Text})
	}
	for _, tc := range m.ToolCalls {
		args := json.RawMessage(tc.Arguments)
		if tc.Arguments == "" {
			args = json.RawMessage("{}")
		}
		parts = append(parts, part{FunctionCall: &functionCall{Name: tc.Name, Args: args}})
	}
	return content{Role: role, Parts: parts}
}

func encodeBlock(b ir.ContentBlock) part {
	switch v := b.(type) {
	case *ir.TextBlock:
		return part{Text: v.Text}
	case *ir.ThinkingBlock:
		return part{Text: v.Thinking, Thought: true}
	case *ir.ToolUseBlock:
		input := v.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		return part{FunctionCall: &functionCall{Name: v.Name, Args: input}}
	case *ir.ToolResultBlock:
		return part{FunctionResponse: &functionResp{Name: v.ToolUseID, Response: geminiFunctionResponse(v.Content)}}
	case *ir.ImageBlock:
		if img, ok := v.Source.(*ir.Base64Media); ok {
			return part{InlineData: &inlineData{MimeType: img.MediaType, Data: img.Data}}
		}
	}
	return part{Text: ""}
}

func mustJSONRaw(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// geminiFunctionResponse ensures a functionResponse.response is a JSON object:
// Gemini rejects scalars/strings/arrays there with a 400. A result that is
// already an object passes through; anything else (a bare string tool result,
// a number, an array) is wrapped as {"result": <value>}.
func geminiFunctionResponse(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if strings.HasPrefix(t, "{") && json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	var v any
	if t != "" && json.Valid([]byte(t)) {
		_ = json.Unmarshal([]byte(t), &v)
	} else {
		v = string(raw)
	}
	b, _ := json.Marshal(map[string]any{"result": v})
	return b
}

// sanitizeGeminiSchema strips keys that Gemini rejects ($schema, $ref, ref,
// definitions, $defs, additionalProperties) recursively from a JSON Schema.
// Without this, Gemini returns 400 for standard JSON-Schema tools. Ported from
// Rust encoder.rs sanitize_gemini_schema.
func sanitizeGeminiSchema(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return v
	}
	var parsed any
	if json.Unmarshal(v, &parsed) != nil {
		return v
	}
	out, err := json.Marshal(sanitizeSchemaValue(parsed))
	if err != nil {
		return v
	}
	return out
}

func sanitizeSchemaValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "$schema", "$ref", "ref", "definitions", "$defs", "additionalProperties":
				continue
			case "type":
				out[k] = sanitizeSchemaType(val)
				continue
			}
			out[k] = sanitizeSchemaValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeSchemaValue(val)
		}
		return out
	default:
		return v
	}
}

// sanitizeSchemaType upper-cases Gemini's schema "type" values, which must be
// uppercase (STRING/OBJECT/ARRAY/...) per the function-declaration + response
// schema spec. Accepts a single string or an array of strings.
func sanitizeSchemaType(val any) any {
	switch t := val.(type) {
	case string:
		return strings.ToUpper(t)
	case []any:
		out := make([]any, len(t))
		for i, s := range t {
			if str, ok := s.(string); ok {
				out[i] = strings.ToUpper(str)
			} else {
				out[i] = sanitizeSchemaValue(s)
			}
		}
		return out
	default:
		return sanitizeSchemaValue(val)
	}
}
