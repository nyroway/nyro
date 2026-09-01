package chatcompletions

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

// requestDecoder implements protocol.RequestDecoder for OpenAI chat completions.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*llm.ChatRequest, error) {
	var w chatRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := llm.NewChatRequest(w.Model, nil)

	msgs := make([]llm.Message, 0, len(w.Messages))
	for _, m := range w.Messages {
		msgs = append(msgs, decodeMessage(m))
	}
	req.Messages = msgs

	maxTok := w.MaxTokens
	if maxTok == nil {
		maxTok = w.MaxCompletionTokens // o1/o3 models send max_completion_tokens only
	}
	req.Generation = llm.GenerationConfig{
		MaxTokens:        maxTok,
		Temperature:      w.Temperature,
		TopP:             w.TopP,
		Seed:             w.Seed,
		Stop:             decodeStop(w.Stop),
		PresencePenalty:  w.PresencePenalty,
		FrequencyPenalty: w.FrequencyPenalty,
	}

	if w.Stream != nil {
		req.Stream.Enabled = *w.Stream
	}
	if w.StreamOptions != nil && w.StreamOptions.IncludeUsage {
		req.Stream.IncludeUsage = true
	}

	for _, t := range w.Tools {
		req.Tools = append(req.Tools, decodeTool(t))
	}
	if len(w.ToolChoice) > 0 {
		req.ToolChoice = decodeToolChoice(w.ToolChoice)
	}
	if w.ParallelToolCalls != nil {
		req.ParallelToolCalls = w.ParallelToolCalls
	}
	if len(w.ResponseFormat) > 0 {
		req.ResponseFormat = decodeResponseFormat(w.ResponseFormat)
	}
	req.Reasoning = decodeReasoning(w.ReasoningEffort, w.Reasoning)

	if ext := buildExt(&w); ext != nil {
		req.Ext = ext
	}
	return req, nil
}

func decodeMessage(m chatMessage) llm.Message {
	msg := llm.Message{Role: decodeRole(m.Role), ToolCallID: m.ToolCallID}
	if len(m.Content) > 0 {
		msg.Content = decodeContent(m.Content)
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return msg
}

func decodeRole(s string) llm.Role {
	switch s {
	case "system", "developer":
		return llm.RoleSystem
	case "assistant":
		return llm.RoleAssistant
	case "tool":
		return llm.RoleTool
	default:
		return llm.RoleUser
	}
}

// decodeContent parses an OpenAI message content field, which is either a
// plain string or an array of typed content parts.
func decodeContent(raw json.RawMessage) llm.MessageContent {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &llm.TextContent{Text: s}
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var blocks []llm.ContentBlock
		for _, p := range parts {
			switch p.Type {
			case "", "text":
				blocks = append(blocks, &llm.TextBlock{Text: p.Text})
			case "image_url":
				if p.ImageURL != nil {
					blocks = append(blocks, decodeImageURL(p.ImageURL.URL))
				}
			}
		}
		return &llm.BlocksContent{Blocks: blocks}
	}
	return &llm.TextContent{}
}

// decodeStop parses the stop field, which is either a string or []string.
func decodeStop(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}

func decodeTool(t chatTool) llm.ToolSpec {
	return llm.ToolSpec{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
		Strict:      t.Function.Strict,
	}
}

func decodeToolChoice(raw json.RawMessage) llm.ToolChoice {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return &llm.AutoToolChoice{}
		case "none":
			return &llm.NoneToolChoice{}
		case "required":
			return &llm.RequiredToolChoice{}
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Type == "function" && obj.Function.Name != "" {
		return &llm.NamedToolChoice{Name: obj.Function.Name}
	}
	return &llm.RawToolChoice{Raw: raw}
}

func decodeResponseFormat(raw json.RawMessage) llm.ResponseFormat {
	var obj struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	switch obj.Type {
	case "text":
		return &llm.TextResponse{}
	case "json_object":
		return &llm.JsonObjectResponse{}
	case "json_schema":
		return &llm.JsonSchemaResponse{
			Name:   obj.JSONSchema.Name,
			Schema: obj.JSONSchema.Schema,
			Strict: obj.JSONSchema.Strict,
		}
	}
	return nil
}

func decodeReasoning(legacyEffort string, reasoning json.RawMessage) llm.ReasoningConfig {
	effort := legacyEffort
	if len(reasoning) > 0 {
		var obj struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(reasoning, &obj); err == nil && obj.Effort != "" {
			effort = obj.Effort
		}
	}
	cfg := llm.ReasoningConfig{}
	if effort != "" {
		cfg.Enabled = true
		cfg.Effort = decodeEffort(effort)
	}
	return cfg
}

func decodeEffort(s string) llm.ReasoningEffort {
	switch s {
	case "none":
		return &llm.ReasoningNone{}
	case "minimal":
		return &llm.ReasoningMinimal{}
	case "low":
		return &llm.ReasoningLow{}
	case "medium":
		return &llm.ReasoningMedium{}
	case "high":
		return &llm.ReasoningHigh{}
	case "xhigh":
		return &llm.ReasoningXhigh{}
	}
	return nil
}

// buildExt collects OpenAIChatExt-only fields (n, logprobs, logit_bias,
// top_logprobs) so they round-trip without landing on the core IR.
func buildExt(w *chatRequest) *llm.OpenAIChatExt {
	if w.N == nil && w.Logprobs == nil && w.TopLogprobs == nil && len(w.LogitBias) == 0 && w.ServiceTier == "" &&
		len(w.Modalities) == 0 && len(w.Prediction) == 0 && len(w.Audio) == 0 && len(w.WebSearchOptions) == 0 &&
		w.PromptCacheRetention == "" && w.Verbosity == "" {
		return nil
	}
	return &llm.OpenAIChatExt{
		N:                    w.N,
		Logprobs:             w.Logprobs,
		TopLogprobs:          w.TopLogprobs,
		LogitBias:            w.LogitBias,
		ServiceTier:          w.ServiceTier,
		Modalities:           w.Modalities,
		Prediction:           w.Prediction,
		Audio:                w.Audio,
		WebSearchOptions:     w.WebSearchOptions,
		PromptCacheRetention: w.PromptCacheRetention,
		Verbosity:            w.Verbosity,
	}
}

// decodeImageURL parses an OpenAI image_url part (data: URL or https URL) into
// an IR ImageBlock.
func decodeImageURL(url string) llm.ContentBlock {
	if strings.HasPrefix(url, "data:") {
		rest := url[5:] // strip "data:"
		semi := strings.Index(rest, ";")
		comma := strings.Index(rest, ",")
		if semi > 0 && comma > semi {
			mediaType := rest[:semi]
			data := rest[comma+1:]
			return &llm.ImageBlock{Source: &llm.Base64Media{MediaType: mediaType, Data: data}}
		}
	}
	return &llm.ImageBlock{Source: &llm.URLMedia{URL: url}}
}
