package generatecontent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

// requestDecoder implements codec.RequestDecoder and codec.PathDecoder.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*llm.ChatRequest, error) {
	return requestDecoder{}.decode(body, "gemini-2.0-flash", false)
}

// DecodeWithPath reads the model and stream flag out of Gemini's single-segment
// {resource} parameter, which carries "{model}:{action}" — e.g.
// "gemini-3.1-flash:streamGenerateContent".
func (d requestDecoder) DecodeWithPath(body []byte, params map[string]string) (*llm.ChatRequest, error) {
	model, action, ok := strings.Cut(params["resource"], ":")
	if !ok || model == "" {
		return nil, fmt.Errorf("malformed Gemini path, expected models/{model}:{action}")
	}
	return d.decode(body, model, action == "streamGenerateContent")
}

func (requestDecoder) decode(body []byte, model string, stream bool) (*llm.ChatRequest, error) {
	var w request
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := llm.NewChatRequest(model, nil)
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
		req.Generation = llm.GenerationConfig{
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
			req.Tools = append(req.Tools, llm.ToolSpec{
				Name: fd.Name, Description: fd.Description, Parameters: fd.Parameters,
			})
		}
	}
	for _, ss := range w.SafetySettings {
		req.SafetySettings = append(req.SafetySettings, llm.SafetySettings{
			Category: ss.Category, Threshold: ss.Threshold,
		})
	}

	if w.GenerationConfig != nil || len(w.ToolConfig) > 0 || w.CachedContent != "" {
		ext := &llm.GoogleExt{
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

func decodeContent(c content) (llm.Message, bool) {
	role := llm.RoleUser
	if c.Role == "model" {
		role = llm.RoleAssistant
	}
	var blocks []llm.ContentBlock
	var toolCalls []llm.ToolCall
	hasFnResp := false
	for _, p := range c.Parts {
		switch {
		case p.FunctionCall != nil:
			id := "call_" + p.FunctionCall.Name // Gemini has no call id; synthesize
			toolCalls = append(toolCalls, llm.ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: string(p.FunctionCall.Args)})
			blocks = append(blocks, &llm.ToolUseBlock{ID: id, Name: p.FunctionCall.Name, Input: p.FunctionCall.Args, ThoughtSignature: p.ThoughtSignature})
		case p.FunctionResponse != nil:
			hasFnResp = true
			blocks = append(blocks, &llm.ToolResultBlock{ToolUseID: p.FunctionResponse.Name, Content: p.FunctionResponse.Response})
		case p.InlineData != nil:
			blocks = append(blocks, &llm.ImageBlock{Source: &llm.Base64Media{MediaType: p.InlineData.MimeType, Data: p.InlineData.Data}})
		case p.Thought:
			blocks = append(blocks, &llm.ThinkingBlock{Thinking: p.Text})
		default:
			if p.Text != "" {
				blocks = append(blocks, &llm.TextBlock{Text: p.Text})
			}
		}
	}
	if len(blocks) == 0 {
		return llm.Message{}, false
	}
	if hasFnResp {
		role = llm.RoleTool
	}
	return llm.Message{Role: role, Content: blockContent(blocks), ToolCalls: toolCalls}, true
}

// blockContent collapses to TextContent when the content is a single text block.
func blockContent(blocks []llm.ContentBlock) llm.MessageContent {
	if len(blocks) == 1 {
		if t, ok := blocks[0].(*llm.TextBlock); ok {
			return &llm.TextContent{Text: t.Text}
		}
	}
	return &llm.BlocksContent{Blocks: blocks}
}

func int64ptr(u *uint32) *int64 {
	if u == nil {
		return nil
	}
	v := int64(*u)
	return &v
}
