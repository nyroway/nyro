package gemini

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestDecoder implements codec.RequestDecoder and codec.PathModelDecoder.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*ir.AiRequest, error) {
	return requestDecoder{}.DecodeWithModel(body, "gemini-2.0-flash", false)
}

func (requestDecoder) DecodeWithModel(body []byte, model string, stream bool) (*ir.AiRequest, error) {
	var w request
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := ir.NewAiRequest(model, nil)
	req.Meta.SourceProtocol = &ids.GoogleGeminiGenerateContentV1Beta
	req.Stream.Enabled = stream

	if w.SystemInstruction != nil {
		req.System = contentText(*w.SystemInstruction)
	}
	for _, c := range w.Contents {
		if m, ok := decodeContent(c); ok {
			req.Messages = append(req.Messages, m)
		}
	}

	if w.GenerationConfig != nil {
		gc := w.GenerationConfig
		req.Generation = ir.GenerationConfig{
			Temperature: gc.Temperature,
			MaxTokens:   gc.MaxOutputTokens,
			TopP:        gc.TopP,
			Seed:        int64ptr(gc.Seed),
			Stop:        gc.StopSequences,
		}
		if len(gc.ThinkingConfig) > 0 {
			var tc struct {
				ThinkingBudget *uint32 `json:"thinkingBudget"`
			}
			if json.Unmarshal(gc.ThinkingConfig, &tc) == nil && tc.ThinkingBudget != nil {
				req.Reasoning.Enabled = true
				req.Reasoning.BudgetTokens = tc.ThinkingBudget
			}
		}
	}

	for _, te := range w.Tools {
		for _, fd := range te.FunctionDeclarations {
			req.Tools = append(req.Tools, ir.ToolSpec{
				Name: fd.Name, Description: fd.Description, Parameters: fd.Parameters,
			})
		}
	}
	for _, ss := range w.SafetySettings {
		req.SafetySettings = append(req.SafetySettings, ir.SafetySettings{
			Category: ss.Category, Threshold: ss.Threshold,
		})
	}

	if w.GenerationConfig != nil || len(w.ToolConfig) > 0 || w.CachedContent != "" {
		ext := &ir.GoogleExt{
			ToolConfig:    w.ToolConfig,
			CachedContent: w.CachedContent,
		}
		if w.GenerationConfig != nil {
			gc := w.GenerationConfig
			ext.TopK = gc.TopK
			ext.CandidateCount = gc.CandidateCount
			ext.ResponseLogprobs = gc.ResponseLogprobs
			ext.Logprobs = gc.Logprobs
			ext.ResponseMimeType = gc.ResponseMimeType
			ext.ResponseJSONSchema = gc.ResponseSchema
			ext.ThinkingConfig = gc.ThinkingConfig
			ext.ResponseModalities = gc.ResponseModalities
			ext.ImageConfig = gc.ImageConfig
		}
		if ext.TopK != nil || ext.CandidateCount != nil || ext.ResponseLogprobs != nil ||
			ext.Logprobs != nil || ext.ResponseMimeType != "" || len(ext.ResponseJSONSchema) > 0 ||
			len(ext.ThinkingConfig) > 0 || len(ext.ToolConfig) > 0 || ext.CachedContent != "" ||
			len(ext.ResponseModalities) > 0 || len(ext.ImageConfig) > 0 {
			req.Ext = ext
		}
	}
	return req, nil
}

// contentText joins all text parts of a content (used for system instruction).
func contentText(c content) string {
	var texts []string
	for _, p := range c.Parts {
		if p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func decodeContent(c content) (ir.Message, bool) {
	role := ir.RoleUser
	if c.Role == "model" {
		role = ir.RoleAssistant
	}
	var blocks []ir.ContentBlock
	var toolCalls []ir.ToolCall
	hasFnResp := false
	for _, p := range c.Parts {
		switch {
		case p.FunctionCall != nil:
			id := "call_" + p.FunctionCall.Name // Gemini has no call id; synthesize
			toolCalls = append(toolCalls, ir.ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: string(p.FunctionCall.Args)})
			blocks = append(blocks, &ir.ToolUseBlock{ID: id, Name: p.FunctionCall.Name, Input: p.FunctionCall.Args})
		case p.FunctionResponse != nil:
			hasFnResp = true
			blocks = append(blocks, &ir.ToolResultBlock{ToolUseID: p.FunctionResponse.Name, Content: p.FunctionResponse.Response})
		case p.InlineData != nil:
			blocks = append(blocks, &ir.ImageBlock{Source: &ir.Base64Media{MediaType: p.InlineData.MimeType, Data: p.InlineData.Data}})
		case p.Thought:
			blocks = append(blocks, &ir.ThinkingBlock{Thinking: p.Text})
		default:
			if p.Text != "" {
				blocks = append(blocks, &ir.TextBlock{Text: p.Text})
			}
		}
	}
	if len(blocks) == 0 {
		return ir.Message{}, false
	}
	if hasFnResp {
		role = ir.RoleTool
	}
	return ir.Message{Role: role, Content: blockContent(blocks), ToolCalls: toolCalls}, true
}

// blockContent collapses to TextContent when the content is a single text block.
func blockContent(blocks []ir.ContentBlock) ir.MessageContent {
	if len(blocks) == 1 {
		if t, ok := blocks[0].(*ir.TextBlock); ok {
			return &ir.TextContent{Text: t.Text}
		}
	}
	return &ir.BlocksContent{Blocks: blocks}
}

func int64ptr(u *uint32) *int64 {
	if u == nil {
		return nil
	}
	v := int64(*u)
	return &v
}
