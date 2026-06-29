package responses

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// streamResponseDecoder parses Responses SSE events (typed by the "type" field).
type streamResponseDecoder struct {
	id, model string
	done      bool
	stop      string
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
	switch {
	case ev.Type == "response.created" || ev.Type == "response.in_progress":
		if ev.Response != nil {
			d.id, d.model = ev.Response.ID, ev.Response.Model
			out = append(out, &ir.MessageStartDelta{ID: ev.Response.ID, Model: ev.Response.Model})
		}
	case ev.Type == "response.output_text.delta":
		if ev.Delta != "" {
			out = append(out, &ir.TextDelta{Text: ev.Delta})
		}
	case ev.Type == "response.function_call_arguments.delta":
		out = append(out, &ir.ToolCallDeltaDelta{Index: 0, Arguments: ev.Delta})
	case ev.Type == "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			out = append(out, &ir.ToolCallStartDelta{Index: 0, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
	case ev.Type == "response.completed":
		if ev.Response != nil {
			if ev.Response.Usage != nil {
				out = append(out, &ir.UsageDelta{Usage: ir.Usage{
					PromptTokens:     ev.Response.Usage.InputTokens,
					CompletionTokens: ev.Response.Usage.OutputTokens,
					TotalTokens:      ev.Response.Usage.TotalTokens,
				}})
			}
			d.stop = ev.Response.Status
		}
		if !d.done {
			d.done = true
			out = append(out, &ir.DoneDelta{StopReason: nvl(d.stop, "completed")})
		}
	case ev.Type == "response.failed" || ev.Type == "response.incomplete":
		if !d.done {
			d.done = true
			out = append(out, &ir.DoneDelta{StopReason: ev.Type})
		}
	}
	return out, nil
}

func (d *streamResponseDecoder) Finish() []ir.StreamDelta {
	if d.done {
		return nil
	}
	d.done = true
	return []ir.StreamDelta{&ir.DoneDelta{StopReason: nvl(d.stop, "completed")}}
}
