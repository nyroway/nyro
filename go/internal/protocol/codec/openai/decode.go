package openai

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// requestDecoder implements codec.RequestDecoder for OpenAI chat completions.
type requestDecoder struct{}

func (requestDecoder) Decode(body []byte) (*ir.AiRequest, error) {
	var w chatRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}

	req := ir.NewAiRequest(w.Model, nil)
	req.Meta.SourceProtocol = &ids.OpenAICompatibleChatCompletionsV1

	msgs := make([]ir.Message, 0, len(w.Messages))
	for _, m := range w.Messages {
		msgs = append(msgs, decodeMessage(m))
	}
	req.Messages = msgs

	maxTok := w.MaxTokens
	if maxTok == nil {
		maxTok = w.MaxCompletionTokens // o1/o3 models send max_completion_tokens only
	}
	req.Generation = ir.GenerationConfig{
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

func decodeMessage(m chatMessage) ir.Message {
	msg := ir.Message{Role: decodeRole(m.Role), ToolCallID: m.ToolCallID}
	if len(m.Content) > 0 {
		msg.Content = decodeContent(m.Content)
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return msg
}

func decodeRole(s string) ir.Role {
	switch s {
	case "system", "developer":
		return ir.RoleSystem
	case "assistant":
		return ir.RoleAssistant
	case "tool":
		return ir.RoleTool
	default:
		return ir.RoleUser
	}
}

// decodeContent parses an OpenAI message content field, which is either a
// plain string or an array of typed content parts.
func decodeContent(raw json.RawMessage) ir.MessageContent {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &ir.TextContent{Text: s}
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var blocks []ir.ContentBlock
		for _, p := range parts {
			switch p.Type {
			case "", "text":
				blocks = append(blocks, &ir.TextBlock{Text: p.Text})
			case "image_url":
				if p.ImageURL != nil {
					blocks = append(blocks, decodeImageURL(p.ImageURL.URL))
				}
			}
		}
		return &ir.BlocksContent{Blocks: blocks}
	}
	return &ir.TextContent{}
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

func decodeTool(t chatTool) ir.ToolSpec {
	return ir.ToolSpec{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
		Strict:      t.Function.Strict,
	}
}

func decodeToolChoice(raw json.RawMessage) ir.ToolChoice {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return &ir.AutoToolChoice{}
		case "none":
			return &ir.NoneToolChoice{}
		case "required":
			return &ir.RequiredToolChoice{}
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Type == "function" && obj.Function.Name != "" {
		return &ir.NamedToolChoice{Name: obj.Function.Name}
	}
	return &ir.RawToolChoice{Raw: raw}
}

func decodeResponseFormat(raw json.RawMessage) ir.ResponseFormat {
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
		return &ir.TextResponse{}
	case "json_object":
		return &ir.JsonObjectResponse{}
	case "json_schema":
		return &ir.JsonSchemaResponse{
			Name:   obj.JSONSchema.Name,
			Schema: obj.JSONSchema.Schema,
			Strict: obj.JSONSchema.Strict,
		}
	}
	return nil
}

func decodeReasoning(legacyEffort string, reasoning json.RawMessage) ir.ReasoningConfig {
	effort := legacyEffort
	if len(reasoning) > 0 {
		var obj struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(reasoning, &obj); err == nil && obj.Effort != "" {
			effort = obj.Effort
		}
	}
	cfg := ir.ReasoningConfig{}
	if effort != "" {
		cfg.Enabled = true
		cfg.Effort = decodeEffort(effort)
	}
	return cfg
}

func decodeEffort(s string) ir.ReasoningEffort {
	switch s {
	case "none":
		return &ir.ReasoningNone{}
	case "minimal":
		return &ir.ReasoningMinimal{}
	case "low":
		return &ir.ReasoningLow{}
	case "medium":
		return &ir.ReasoningMedium{}
	case "high":
		return &ir.ReasoningHigh{}
	case "xhigh":
		return &ir.ReasoningXhigh{}
	}
	return nil
}

// buildExt collects OpenAIChatExt-only fields (n, logprobs, logit_bias,
// top_logprobs) so they round-trip without landing on the core IR.
func buildExt(w *chatRequest) *ir.OpenAIChatExt {
	if w.N == nil && w.Logprobs == nil && w.TopLogprobs == nil && len(w.LogitBias) == 0 && w.ServiceTier == "" &&
		len(w.Modalities) == 0 && len(w.Prediction) == 0 && len(w.Audio) == 0 && len(w.WebSearchOptions) == 0 &&
		w.PromptCacheRetention == "" && w.Verbosity == "" {
		return nil
	}
	return &ir.OpenAIChatExt{
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
func decodeImageURL(url string) ir.ContentBlock {
	if strings.HasPrefix(url, "data:") {
		rest := url[5:] // strip "data:"
		semi := strings.Index(rest, ";")
		comma := strings.Index(rest, ",")
		if semi > 0 && comma > semi {
			mediaType := rest[:semi]
			data := rest[comma+1:]
			return &ir.ImageBlock{Source: &ir.Base64Media{MediaType: mediaType, Data: data}}
		}
	}
	return &ir.ImageBlock{Source: &ir.URLMedia{URL: url}}
}
