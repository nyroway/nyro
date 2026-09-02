package responses

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// streamResponseEncoder formats IR deltas as the full Responses API SSE event
// sequence: response.created → output_item.added → content_part.added →
// output_text.delta × N → output_text.done → content_part.done →
// output_item.done → response.completed. Ported from the Rust
// ResponsesStreamFormatter.
type streamResponseEncoder struct {
	id, model string
	itemID    string
	itemAdded bool
	partAdded bool
	usage     llm.Usage
	textBuf   strings.Builder
}

func (e *streamResponseEncoder) FormatDeltas(deltas []llm.StreamDelta) ([]protocol.Event, error) {
	var out []protocol.Event
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

// FormatDone is a no-op: response.completed is emitted on the DoneDelta.
func (e *streamResponseEncoder) FormatDone(_ llm.Usage) ([]protocol.Event, error) {
	return nil, nil
}

func (e *streamResponseEncoder) formatDelta(d llm.StreamDelta) []protocol.Event {
	switch v := d.(type) {
	case *llm.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.itemID = "msg_" + v.ID
		return []protocol.Event{
			ev(map[string]any{"type": "response.created", "response": map[string]any{"id": v.ID, "model": v.Model, "status": "in_progress"}}),
			ev(map[string]any{"type": "response.in_progress", "response": map[string]string{"id": v.ID, "status": "in_progress"}}),
		}
	case *llm.TextDelta:
		var out []protocol.Event
		if !e.itemAdded {
			e.itemAdded = true
			out = append(out, ev(map[string]any{
				"type": "response.output_item.added", "output_index": 0,
				"item": map[string]any{"type": "message", "id": e.itemID, "role": "assistant", "status": "in_progress", "content": []any{}},
			}))
		}
		if !e.partAdded {
			e.partAdded = true
			out = append(out, ev(map[string]any{
				"type": "response.content_part.added", "item_id": e.itemID, "output_index": 0, "content_index": 0,
				"part": map[string]string{"type": "output_text", "text": ""},
			}))
		}
		e.textBuf.WriteString(v.Text)
		out = append(out, ev(map[string]any{
			"type": "response.output_text.delta", "item_id": e.itemID, "output_index": 0, "content_index": 0,
			"delta": v.Text,
		}))
		return out
	case *llm.UsageDelta:
		e.usage = v.Usage
		return nil
	case *llm.DoneDelta:
		fullText := e.textBuf.String()
		usage := e.usage
		if v.UsageAtDone != nil {
			usage = *v.UsageAtDone
		}
		return []protocol.Event{
			ev(map[string]any{"type": "response.output_text.done", "item_id": e.itemID, "output_index": 0, "content_index": 0, "text": fullText}),
			ev(map[string]any{"type": "response.content_part.done", "item_id": e.itemID, "output_index": 0, "content_index": 0, "part": map[string]string{"type": "output_text", "text": fullText}}),
			ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "id": e.itemID, "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": fullText}}}}),
			ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": e.id, "model": e.model, "status": "completed", "usage": map[string]uint32{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens}}}),
		}
	}
	return nil
}

func ev(payload any) protocol.Event {
	b, _ := json.Marshal(payload)
	return protocol.Event{Data: string(b)}
}
