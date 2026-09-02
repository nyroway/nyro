package responses

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// streamResponseDecoder parses Responses SSE events (typed by the "type" field).
type streamResponseDecoder struct {
	id, model string
	done      bool
	stop      string
	sawTool   bool // a function_call item appeared → stop reason is tool_calls
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
	case "response.created", "response.in_progress":
		if ev.Response != nil {
			d.id, d.model = ev.Response.ID, ev.Response.Model
			out = append(out, &llm.MessageStartDelta{ID: ev.Response.ID, Model: ev.Response.Model})
		}
	case "response.output_text.delta":
		if ev.Delta != "" {
			out = append(out, &llm.TextDelta{Text: ev.Delta})
		}
	case "response.function_call_arguments.delta":
		out = append(out, &llm.ToolCallDeltaDelta{Index: 0, Arguments: ev.Delta})
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			d.sawTool = true
			out = append(out, &llm.ToolCallStartDelta{Index: 0, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
	case "response.completed":
		if ev.Response != nil {
			if ev.Response.Usage != nil {
				out = append(out, &llm.UsageDelta{Usage: llm.Usage{
					PromptTokens:     ev.Response.Usage.InputTokens,
					CompletionTokens: ev.Response.Usage.OutputTokens,
					TotalTokens:      ev.Response.Usage.TotalTokens,
				}})
			}
			d.stop = responsesStopReason(ev.Response.Status, d.sawTool)
		}
		if !d.done {
			d.done = true
			out = append(out, &llm.DoneDelta{StopReason: nvl(d.stop, "stop")})
		}
	case "response.incomplete":
		// Raw event type is not a valid downstream stop reason; incomplete is
		// almost always max_output_tokens → length.
		if !d.done {
			d.done = true
			out = append(out, &llm.DoneDelta{StopReason: "length"})
		}
	case "response.failed":
		if d.done {
			break
		}
		d.done = true
		var detail responseError
		if ev.Response != nil && ev.Response.Error != nil {
			detail = *ev.Response.Error
		}
		out = append(out, streamErrorDelta(payload, detail.Code, detail.Message))
	case "error":
		if d.done {
			break
		}
		d.done = true
		out = append(out, streamErrorDelta(payload, ev.Code, ev.Message))
	}
	return out, nil
}

func streamErrorDelta(payload, code, message string) llm.StreamDelta {
	providerError := protocol.ErrorFromWire(
		protocol.WireResponse{Body: []byte(payload)},
		message,
		code,
	)
	return &llm.StreamErrorDelta{Error: providerError}
}

func (d *streamResponseDecoder) Finish() []llm.StreamDelta {
	if d.done {
		return nil
	}
	d.done = true
	return []llm.StreamDelta{&llm.DoneDelta{StopReason: nvl(d.stop, "stop")}}
}
