package messages

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// streamResponseDecoder implements protocol.StreamResponseDecoder for Anthropic
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

func (d *streamResponseDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	if payload == "" {
		return nil, nil
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return []llm.StreamDelta{&llm.UnknownDelta{Raw: payload}}, nil
	}
	var out []llm.StreamDelta
	switch ev.Type {
	case "message_start":
		var p messageStartPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			d.id, d.model, d.started = p.Message.ID, p.Message.Model, true
			d.inputTokens = p.Message.Usage.InputTokens
			d.cacheReadTokens = p.Message.Usage.CacheReadTokens
			d.cacheCreationTokens = p.Message.Usage.CacheCreationTokens
			out = append(out, &llm.MessageStartDelta{ID: p.Message.ID, Model: p.Message.Model})
		}
	case "content_block_start":
		var p contentBlockStartPayload
		if json.Unmarshal([]byte(payload), &p) == nil && p.ContentBlock.Type == "tool_use" {
			out = append(out, &llm.ToolCallStartDelta{Index: p.Index, ID: p.ContentBlock.ID, Name: p.ContentBlock.Name})
		}
	case "content_block_delta":
		var p contentBlockDeltaPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			var dp deltaPayload
			_ = json.Unmarshal(p.Delta, &dp)
			switch dp.Type {
			case "text_delta":
				out = append(out, &llm.TextDelta{Text: dp.Text})
			case "thinking_delta":
				out = append(out, &llm.ThinkingDelta{Text: dp.Thinking})
			case "input_json_delta":
				out = append(out, &llm.ToolCallDeltaDelta{Index: p.Index, Arguments: dp.PartialJSON})
			case "signature_delta":
				out = append(out, &llm.ThinkingSignatureDelta{Signature: dp.Signature})
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
		out = append(out, &llm.UsageDelta{Usage: llm.Usage{
			PromptTokens:        d.inputTokens,
			CompletionTokens:    d.outputTokens,
			TotalTokens:         d.inputTokens + d.outputTokens,
			CacheReadTokens:     d.cacheReadTokens,
			CacheCreationTokens: d.cacheCreationTokens,
		}})
		if !d.done {
			d.done = true
			out = append(out, &llm.DoneDelta{StopReason: d.stop})
		}
	case "error":
		providerError, _ := (egress{}).DecodeError(protocol.WireResponse{Body: []byte(payload)})
		d.done = true
		out = append(out, &llm.StreamErrorDelta{Error: providerError})
	default:
		// Forward unknown events verbatim (server_tool_use, citations_delta,
		// web_search_tool_result, future events) — "no silent drops" principle.
		out = append(out, &llm.UnknownDelta{Raw: payload})
	}
	return out, nil
}

func (d *streamResponseDecoder) Finish() []llm.StreamDelta {
	if d.done {
		return nil
	}
	d.done = true
	return []llm.StreamDelta{&llm.DoneDelta{StopReason: d.stop}}
}

// streamResponseEncoder implements protocol.StreamResponseEncoder for Anthropic,
// managing the content-block lifecycle (content_block_start/delta/stop).
type streamResponseEncoder struct {
	id, model string
	created   int64
	openType  string // "", "text", "thinking", "tool_use"
	openIndex uint
	nextIndex uint
	usage     llm.Usage
}

func (e *streamResponseEncoder) FormatDeltas(deltas []llm.StreamDelta) ([]protocol.Event, error) {
	var out []protocol.Event
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

// FormatDone is a no-op for Anthropic: the terminal message_delta (with usage)
// and message_stop are emitted when the DoneDelta is processed in FormatDeltas.
func (e *streamResponseEncoder) FormatDone(_ llm.Usage) ([]protocol.Event, error) {
	return nil, nil
}

func (e *streamResponseEncoder) formatDelta(d llm.StreamDelta) []protocol.Event {
	switch v := d.(type) {
	case *llm.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.created = nowUnix()
		var p messageStartPayload
		p.Type = "message_start"
		p.Message.ID = v.ID
		p.Message.Model = v.Model
		return []protocol.Event{sse("message_start", p), sse("ping", streamEvent{Type: "ping"})}
	case *llm.TextDelta:
		out := e.ensureBlock("text", contentBlock{Type: "text"})
		return append(out, deltaSSE(e.openIndex, deltaPayload{Type: "text_delta", Text: v.Text}))
	case *llm.ThinkingDelta:
		out := e.ensureBlock("thinking", contentBlock{Type: "thinking"})
		return append(out, deltaSSE(e.openIndex, deltaPayload{Type: "thinking_delta", Thinking: v.Text}))
	case *llm.ThinkingSignatureDelta:
		return []protocol.Event{deltaSSE(e.openIndex, deltaPayload{Type: "signature_delta", Signature: v.Signature})}
	case *llm.ToolCallStartDelta:
		return e.ensureBlock("tool_use", contentBlock{Type: "tool_use", ID: v.ID, Name: v.Name, Input: json.RawMessage("{}")})
	case *llm.ToolCallDeltaDelta:
		return []protocol.Event{deltaSSE(e.openIndex, deltaPayload{Type: "input_json_delta", PartialJSON: v.Arguments})}
	case *llm.UsageDelta:
		e.usage = v.Usage
		return nil
	case *llm.DoneDelta:
		usage := e.usage
		if v.UsageAtDone != nil {
			usage = *v.UsageAtDone
		}
		return e.terminate(v.StopReason, usage)
	case *llm.UnknownDelta:
		return []protocol.Event{{Data: v.Raw}}
	}
	return nil
}

func (e *streamResponseEncoder) ensureBlock(want string, start contentBlock) []protocol.Event {
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

func (e *streamResponseEncoder) closeOpenBlock() []protocol.Event {
	if e.openType == "" {
		return nil
	}
	out := []protocol.Event{sse("content_block_stop", contentBlockStopPayload{Type: "content_block_stop", Index: e.openIndex})}
	e.openType = ""
	return out
}

func (e *streamResponseEncoder) terminate(stopReason string, usage llm.Usage) []protocol.Event {
	out := e.closeOpenBlock()
	usageMap := map[string]any{"output_tokens": usage.CompletionTokens}
	if usage.CacheReadTokens != nil {
		usageMap["cache_read_input_tokens"] = *usage.CacheReadTokens
	}
	if usage.CacheCreationTokens != nil {
		usageMap["cache_creation_input_tokens"] = *usage.CacheCreationTokens
	}
	md := map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": toAnthropicStopReason(stopReason)},
		"usage": usageMap,
	}
	b, _ := json.Marshal(md)
	out = append(out, protocol.Event{Event: "message_delta", Data: string(b)})
	out = append(out, sse("message_stop", messageStopPayload{Type: "message_stop"}))
	return out
}

func sse(event string, payload any) protocol.Event {
	b, _ := json.Marshal(payload)
	return protocol.Event{Event: event, Data: string(b)}
}

func deltaSSE(index uint, dp deltaPayload) protocol.Event {
	p := map[string]any{"type": "content_block_delta", "index": index, "delta": dp}
	b, _ := json.Marshal(p)
	return protocol.Event{Event: "content_block_delta", Data: string(b)}
}
