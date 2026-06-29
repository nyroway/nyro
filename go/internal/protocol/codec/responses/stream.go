package responses

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
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
	usage     ir.Usage
	textBuf   strings.Builder
}

func (e *streamResponseEncoder) FormatDeltas(deltas []ir.StreamDelta) ([]codec.SSE, error) {
	var out []codec.SSE
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

// FormatDone is a no-op: response.completed is emitted on the DoneDelta.
func (e *streamResponseEncoder) FormatDone(_ ir.Usage) ([]codec.SSE, error) {
	return nil, nil
}

func (e *streamResponseEncoder) formatDelta(d ir.StreamDelta) []codec.SSE {
	switch v := d.(type) {
	case *ir.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.itemID = "msg_" + v.ID
		return []codec.SSE{
			ev(map[string]any{"type": "response.created", "response": map[string]any{"id": v.ID, "model": v.Model, "status": "in_progress"}}),
			ev(map[string]any{"type": "response.in_progress", "response": map[string]string{"id": v.ID, "status": "in_progress"}}),
		}
	case *ir.TextDelta:
		var out []codec.SSE
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
	case *ir.UsageDelta:
		e.usage = v.Usage
		return nil
	case *ir.DoneDelta:
		fullText := e.textBuf.String()
		return []codec.SSE{
			ev(map[string]any{"type": "response.output_text.done", "item_id": e.itemID, "output_index": 0, "content_index": 0, "text": fullText}),
			ev(map[string]any{"type": "response.content_part.done", "item_id": e.itemID, "output_index": 0, "content_index": 0, "part": map[string]string{"type": "output_text", "text": fullText}}),
			ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "id": e.itemID, "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": fullText}}}}),
			ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": e.id, "model": e.model, "status": "completed", "usage": map[string]uint32{"input_tokens": e.usage.PromptTokens, "output_tokens": e.usage.CompletionTokens, "total_tokens": e.usage.TotalTokens}}}),
		}
	}
	return nil
}

func ev(payload any) codec.SSE {
	b, _ := json.Marshal(payload)
	return codec.SSE{Data: string(b)}
}
