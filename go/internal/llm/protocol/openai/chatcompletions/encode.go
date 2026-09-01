package chatcompletions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// requestEncoder implements protocol.RequestEncoder for OpenAI chat completions.
type requestEncoder struct{}

func (requestEncoder) Encode(req *llm.ChatRequest) (protocol.WireRequest, error) {
	w := chatRequest{
		Model:    req.Model,
		Messages: make([]chatMessage, 0, len(req.Messages)),
	}
	for _, m := range normalizeToolCallIDs(req.Messages) {
		w.Messages = append(w.Messages, encodeMessage(m))
	}

	w.MaxTokens = req.Generation.MaxTokens
	w.Temperature = req.Generation.Temperature
	w.TopP = req.Generation.TopP
	w.Seed = req.Generation.Seed
	if len(req.Generation.Stop) > 0 {
		w.Stop = encodeStop(req.Generation.Stop)
	}
	w.PresencePenalty = req.Generation.PresencePenalty
	w.FrequencyPenalty = req.Generation.FrequencyPenalty

	if req.Stream.Enabled {
		b := true
		w.Stream = &b
		w.StreamOptions = &streamOptions{IncludeUsage: true} // always request usage in stream
	}

	for _, t := range req.Tools {
		w.Tools = append(w.Tools, encodeTool(t))
	}
	if req.ToolChoice != nil {
		w.ToolChoice = encodeToolChoice(req.ToolChoice)
	}
	if req.ParallelToolCalls != nil {
		w.ParallelToolCalls = req.ParallelToolCalls
	}
	if req.ResponseFormat != nil {
		w.ResponseFormat = encodeResponseFormat(req.ResponseFormat)
	}
	if effort := encodeReasoning(req.Reasoning); effort != "" {
		w.ReasoningEffort = effort
	}

	if e, ok := req.Ext.(*llm.OpenAIChatExt); ok {
		w.N = e.N
		w.Logprobs = e.Logprobs
		w.TopLogprobs = e.TopLogprobs
		w.LogitBias = e.LogitBias
		w.ServiceTier = e.ServiceTier
		w.Modalities = e.Modalities
		w.Prediction = e.Prediction
		w.Audio = e.Audio
		w.WebSearchOptions = e.WebSearchOptions
		w.PromptCacheRetention = e.PromptCacheRetention
		w.Verbosity = e.Verbosity
	}

	body, err := json.Marshal(w)
	if err != nil {
		return protocol.WireRequest{}, err
	}
	return protocol.WireRequest{
		Method:  "POST",
		Path:    "/v1/chat/completions",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Stream:  req.Stream.Enabled,
	}, nil
}

func encodeMessage(m llm.Message) chatMessage {
	w := chatMessage{Role: string(m.Role), ToolCallID: m.ToolCallID}
	if m.Content != nil {
		// Extract thinking blocks into reasoning_content (for cross-protocol
		// transforms where the source carries thinking as content blocks).
		if bc, ok := m.Content.(*llm.BlocksContent); ok {
			var thinking []string
			for _, b := range bc.Blocks {
				if t, ok := b.(*llm.ThinkingBlock); ok && t.Thinking != "" {
					thinking = append(thinking, t.Thinking)
				}
			}
			if len(thinking) > 0 {
				w.ReasoningContent = strings.Join(thinking, "\n\n")
			}
		}
		if raw, ok := encodeContent(m.Content); ok {
			w.Content = raw
		}
	}
	for _, tc := range m.ToolCalls {
		w.ToolCalls = append(w.ToolCalls, chatToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: chatToolCallFn{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return w
}

// encodeContent renders message content to the OpenAI content field. It returns
// (json, true) when there is content to emit, or (nil, false) to OMIT the
// content field entirely — e.g. an assistant turn carrying only tool calls has
// no textual content; emitting `content:[]` is rejected by strict upstreams
// (regression #233). Non-text blocks (ToolUse → tool_calls, Thinking →
// reasoning_content meta, image → TODO P2) are intentionally not emitted here.
func encodeContent(c llm.MessageContent) (json.RawMessage, bool) {
	switch v := c.(type) {
	case *llm.TextContent:
		b, _ := json.Marshal(v.Text)
		return b, true
	case *llm.BlocksContent:
		var parts []contentPart
		for _, b := range v.Blocks {
			switch blk := b.(type) {
			case *llm.TextBlock:
				parts = append(parts, contentPart{Type: "text", Text: blk.Text})
			case *llm.ImageBlock:
				if img, ok := blk.Source.(*llm.Base64Media); ok {
					parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + img.MediaType + ";base64," + img.Data}})
				} else if u, ok := blk.Source.(*llm.URLMedia); ok {
					parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: u.URL}})
				}
			}
		}
		if len(parts) == 0 {
			return nil, false
		}
		out, _ := json.Marshal(parts)
		return out, true
	}
	return nil, false
}

// normalizeToolCallIDs normalizes tool-call messages before OpenAI encoding:
//  1. prunes orphan tool_calls (the last assistant's calls without matching
//     tool_results — no more results are coming);
//  2. deduplicates tool_call IDs that appear in more than one assistant message
//     (a cross-protocol artifact), keeping the first occurrence and remapping
//     subsequent duplicates + their matching tool_result.
//
// Ported (scoped) from Rust encoder.rs normalize_messages_for_openai.
func normalizeToolCallIDs(msgs []llm.Message) []llm.Message {
	// Pass 1: collect tool_call IDs that have matching tool_results.
	hasResult := map[string]bool{}
	for _, m := range msgs {
		if m.ToolCallID != "" {
			hasResult[m.ToolCallID] = true
		}
	}

	// Pass 2: prune orphan tool_calls from the last assistant message only
	// (non-last messages may still receive results).
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		var kept []llm.ToolCall
		for _, tc := range msgs[i].ToolCalls {
			if hasResult[tc.ID] {
				kept = append(kept, tc)
			}
		}
		msgs[i].ToolCalls = kept
		break
	}

	// Pass 3: dedup IDs.
	idCount := map[string]int{}
	for _, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			idCount[tc.ID]++
		}
	}
	hasDupes := false
	for _, c := range idCount {
		if c > 1 {
			hasDupes = true
			break
		}
	}
	if !hasDupes {
		return msgs
	}
	seen := map[string]bool{}
	counter := 0
	for i := range msgs {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		for j := range msgs[i].ToolCalls {
			id := msgs[i].ToolCalls[j].ID
			if seen[id] {
				newID := fmt.Sprintf("%s_dedup_%d", id, counter)
				counter++
				msgs[i].ToolCalls[j].ID = newID
				for k := i + 1; k < len(msgs); k++ {
					if msgs[k].ToolCallID == id {
						msgs[k].ToolCallID = newID
						break
					}
				}
				seen[newID] = true
			} else {
				seen[id] = true
			}
		}
	}
	return msgs
}

func encodeStop(stops []string) json.RawMessage {
	if len(stops) == 1 {
		b, _ := json.Marshal(stops[0])
		return b
	}
	b, _ := json.Marshal(stops)
	return b
}

func encodeTool(t llm.ToolSpec) chatTool {
	return chatTool{
		Type: "function",
		Function: chatToolFunction{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Strict:      t.Strict,
		},
	}
}

func encodeToolChoice(tc llm.ToolChoice) json.RawMessage {
	switch v := tc.(type) {
	case *llm.AutoToolChoice:
		return json.RawMessage(`"auto"`)
	case *llm.NoneToolChoice:
		return json.RawMessage(`"none"`)
	case *llm.RequiredToolChoice:
		return json.RawMessage(`"required"`)
	case *llm.NamedToolChoice:
		b, _ := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": v.Name},
		})
		return b
	case *llm.RawToolChoice:
		return v.Raw
	}
	return json.RawMessage(`"auto"`)
}

func encodeResponseFormat(rf llm.ResponseFormat) json.RawMessage {
	switch v := rf.(type) {
	case *llm.TextResponse:
		return json.RawMessage(`{"type":"text"}`)
	case *llm.JsonObjectResponse:
		return json.RawMessage(`{"type":"json_object"}`)
	case *llm.JsonSchemaResponse:
		js := map[string]any{"name": v.Name}
		if len(v.Schema) > 0 {
			js["schema"] = v.Schema
		}
		if v.Strict != nil {
			js["strict"] = *v.Strict
		}
		b, _ := json.Marshal(map[string]any{"type": "json_schema", "json_schema": js})
		return b
	}
	return nil
}

func encodeReasoning(r llm.ReasoningConfig) string {
	if !r.Enabled || r.Effort == nil {
		return ""
	}
	switch r.Effort.(type) {
	case *llm.ReasoningNone:
		return "none"
	case *llm.ReasoningMinimal:
		return "minimal"
	case *llm.ReasoningLow:
		return "low"
	case *llm.ReasoningMedium:
		return "medium"
	case *llm.ReasoningHigh:
		return "high"
	case *llm.ReasoningXhigh:
		return "xhigh"
	}
	return ""
}
