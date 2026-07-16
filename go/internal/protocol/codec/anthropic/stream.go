package anthropic

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// streamResponseDecoder implements codec.StreamResponseDecoder for Anthropic
// SSE. Dispatches on the data payload's "type" field.
type streamResponseDecoder struct {
	id, model           string
	started, done       bool
	stop                string
	inputTokens         uint32
	outputTokens        uint32
	cacheReadTokens     *uint32
	cacheCreationTokens *uint32
}

func (d *streamResponseDecoder) ParseChunk(payload string) ([]ir.StreamDelta, error) {
	if payload == "" {
		return nil, nil
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return []ir.StreamDelta{&ir.UnknownDelta{Raw: payload}}, nil
	}
	var out []ir.StreamDelta
	switch ev.Type {
	case "message_start":
		var p messageStartPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			d.id, d.model, d.started = p.Message.ID, p.Message.Model, true
			d.inputTokens = p.Message.Usage.InputTokens
			d.cacheReadTokens = p.Message.Usage.CacheReadTokens
			d.cacheCreationTokens = p.Message.Usage.CacheCreationTokens
			out = append(out, &ir.MessageStartDelta{ID: p.Message.ID, Model: p.Message.Model})
		}
	case "content_block_start":
		var p contentBlockStartPayload
		if json.Unmarshal([]byte(payload), &p) == nil && p.ContentBlock.Type == "tool_use" {
			out = append(out, &ir.ToolCallStartDelta{Index: p.Index, ID: p.ContentBlock.ID, Name: p.ContentBlock.Name})
		}
	case "content_block_delta":
		var p contentBlockDeltaPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			var dp deltaPayload
			_ = json.Unmarshal(p.Delta, &dp)
			switch dp.Type {
			case "text_delta":
				out = append(out, &ir.TextDelta{Text: dp.Text})
			case "thinking_delta":
				out = append(out, &ir.ThinkingDelta{Text: dp.Thinking})
			case "input_json_delta":
				out = append(out, &ir.ToolCallDeltaDelta{Index: p.Index, Arguments: dp.PartialJSON})
			case "signature_delta":
				out = append(out, &ir.ThinkingSignatureDelta{Signature: dp.Signature})
			}
		}
	case "message_delta":
		var p messageDeltaPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			if p.Delta.StopReason != "" {
				d.stop = p.Delta.StopReason
			}
			if p.Usage != nil {
				d.outputTokens = p.Usage.OutputTokens
				// Providers that report the full usage only in message_delta
				// (e.g. Zhipu/GLM, whose message_start usage is all-zero) carry
				// the real input/cache counts here. Adopt them when present;
				// real Anthropic omits these fields (decode to 0 / nil), so the
				// message_start values captured above are preserved.
				if p.Usage.InputTokens != 0 {
					d.inputTokens = p.Usage.InputTokens
				}
				if p.Usage.CacheReadTokens != nil {
					d.cacheReadTokens = p.Usage.CacheReadTokens
				}
				if p.Usage.CacheCreationTokens != nil {
					d.cacheCreationTokens = p.Usage.CacheCreationTokens
				}
			}
		}
	case "message_stop":
		out = append(out, &ir.UsageDelta{Usage: ir.Usage{
			PromptTokens:        d.inputTokens,
			CompletionTokens:    d.outputTokens,
			TotalTokens:         d.inputTokens + d.outputTokens,
			CacheReadTokens:     d.cacheReadTokens,
			CacheCreationTokens: d.cacheCreationTokens,
		}})
		if !d.done {
			d.done = true
			out = append(out, &ir.DoneDelta{StopReason: d.stop})
		}
	default:
		// Forward unknown events verbatim (server_tool_use, citations_delta,
		// web_search_tool_result, future events) — "no silent drops" principle.
		out = append(out, &ir.UnknownDelta{Raw: payload})
	}
	return out, nil
}

func (d *streamResponseDecoder) Finish() []ir.StreamDelta {
	if d.done {
		return nil
	}
	d.done = true
	return []ir.StreamDelta{&ir.DoneDelta{StopReason: d.stop}}
}

// streamResponseEncoder implements codec.StreamResponseEncoder for Anthropic,
// managing the content-block lifecycle (content_block_start/delta/stop).
type streamResponseEncoder struct {
	id, model string
	created   int64
	openType  string // "", "text", "thinking", "tool_use"
	openIndex uint
	nextIndex uint
	usage     ir.Usage
}

func (e *streamResponseEncoder) FormatDeltas(deltas []ir.StreamDelta) ([]codec.SSE, error) {
	var out []codec.SSE
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

// FormatDone is a no-op for Anthropic: the terminal message_delta (with usage)
// and message_stop are emitted when the DoneDelta is processed in FormatDeltas.
func (e *streamResponseEncoder) FormatDone(_ ir.Usage) ([]codec.SSE, error) {
	return nil, nil
}

func (e *streamResponseEncoder) formatDelta(d ir.StreamDelta) []codec.SSE {
	switch v := d.(type) {
	case *ir.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.created = nowUnix()
		var p messageStartPayload
		p.Type = "message_start"
		p.Message.ID = v.ID
		p.Message.Model = v.Model
		return []codec.SSE{sse("message_start", p), sse("ping", streamEvent{Type: "ping"})}
	case *ir.TextDelta:
		out := e.ensureBlock("text", contentBlock{Type: "text"})
		return append(out, deltaSSE(e.openIndex, deltaPayload{Type: "text_delta", Text: v.Text}))
	case *ir.ThinkingDelta:
		out := e.ensureBlock("thinking", contentBlock{Type: "thinking"})
		return append(out, deltaSSE(e.openIndex, deltaPayload{Type: "thinking_delta", Thinking: v.Text}))
	case *ir.ThinkingSignatureDelta:
		return []codec.SSE{deltaSSE(e.openIndex, deltaPayload{Type: "signature_delta", Signature: v.Signature})}
	case *ir.ToolCallStartDelta:
		return e.ensureBlock("tool_use", contentBlock{Type: "tool_use", ID: v.ID, Name: v.Name, Input: json.RawMessage("{}")})
	case *ir.ToolCallDeltaDelta:
		return []codec.SSE{deltaSSE(e.openIndex, deltaPayload{Type: "input_json_delta", PartialJSON: v.Arguments})}
	case *ir.UsageDelta:
		e.usage = v.Usage
		return nil
	case *ir.DoneDelta:
		return e.terminate(v.StopReason)
	case *ir.UnknownDelta:
		return []codec.SSE{{Data: v.Raw}}
	}
	return nil
}

func (e *streamResponseEncoder) ensureBlock(want string, start contentBlock) []codec.SSE {
	if e.openType == want {
		return nil
	}
	out := e.closeOpenBlock()
	e.openType = want
	e.openIndex = e.nextIndex
	e.nextIndex++
	return append(out, sse("content_block_start", contentBlockStartPayload{
		Type: "content_block_start", Index: e.openIndex, ContentBlock: start,
	}))
}

func (e *streamResponseEncoder) closeOpenBlock() []codec.SSE {
	if e.openType == "" {
		return nil
	}
	out := []codec.SSE{sse("content_block_stop", contentBlockStopPayload{Type: "content_block_stop", Index: e.openIndex})}
	e.openType = ""
	return out
}

func (e *streamResponseEncoder) terminate(stopReason string) []codec.SSE {
	out := e.closeOpenBlock()
	usageMap := map[string]any{"output_tokens": e.usage.CompletionTokens}
	if e.usage.CacheReadTokens != nil {
		usageMap["cache_read_input_tokens"] = *e.usage.CacheReadTokens
	}
	if e.usage.CacheCreationTokens != nil {
		usageMap["cache_creation_input_tokens"] = *e.usage.CacheCreationTokens
	}
	md := map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": toAnthropicStopReason(stopReason)},
		"usage": usageMap,
	}
	b, _ := json.Marshal(md)
	out = append(out, codec.SSE{Event: "message_delta", Data: string(b)})
	out = append(out, sse("message_stop", messageStopPayload{Type: "message_stop"}))
	return out
}

func sse(event string, payload any) codec.SSE {
	b, _ := json.Marshal(payload)
	return codec.SSE{Event: event, Data: string(b)}
}

func deltaSSE(index uint, dp deltaPayload) codec.SSE {
	p := map[string]any{"type": "content_block_delta", "index": index, "delta": dp}
	b, _ := json.Marshal(p)
	return codec.SSE{Event: "content_block_delta", Data: string(b)}
}
