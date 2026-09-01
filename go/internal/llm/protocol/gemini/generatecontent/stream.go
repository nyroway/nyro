package generatecontent

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// streamResponseDecoder parses Gemini streamGenerateContent chunks. Each chunk
// is a generateContent-response-shaped object; parts map to deltas and
// finishReason/usageMetadata terminate the stream.
type streamResponseDecoder struct {
	done    bool
	stop    string
	started bool
	sawTool bool // a functionCall part was seen → stop reason is tool_calls
}

func (d *streamResponseDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	if payload == "" {
		return nil, nil
	}
	var chunk response
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return []llm.StreamDelta{&llm.UnknownDelta{Raw: payload}}, nil
	}
	var out []llm.StreamDelta
	if len(chunk.Candidates) > 0 {
		if !d.started {
			d.started = true
			out = append(out, &llm.MessageStartDelta{Model: chunk.ModelVersion})
		}
		c := chunk.Candidates[0]
		for _, p := range c.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				d.sawTool = true
				out = append(out, &llm.ToolCallStartDelta{Index: 0, Name: p.FunctionCall.Name})
			case p.Thought:
				out = append(out, &llm.ThinkingDelta{Text: p.Text})
			default:
				if p.Text != "" {
					out = append(out, &llm.TextDelta{Text: p.Text})
				}
			}
		}
		if c.FinishReason != "" {
			d.stop = normalizeGeminiFinishReason(c.FinishReason)
			// Gemini keeps finishReason=STOP even when returning a functionCall;
			// upgrade so downstream emits tool_use rather than end_turn.
			if d.sawTool && d.stop == "stop" {
				d.stop = "tool_calls"
			}
		}
	}
	if chunk.UsageMetadata != nil {
		out = append(out, &llm.UsageDelta{Usage: llm.Usage{
			PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
			CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
		}})
	}
	if d.stop != "" && !d.done {
		d.done = true
		out = append(out, &llm.DoneDelta{StopReason: d.stop})
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

// streamResponseEncoder formats IR deltas as Gemini streamGenerateContent
// chunks (SSE data frames, one response object per delta).
type streamResponseEncoder struct {
	usage llm.Usage
}

func (e *streamResponseEncoder) FormatDeltas(deltas []llm.StreamDelta) ([]protocol.Event, error) {
	var out []protocol.Event
	for _, d := range deltas {
		if f := e.formatDelta(d); f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

// FormatDone emits a final usageMetadata chunk if usage was captured (Gemini
// has no [DONE] terminator).
func (e *streamResponseEncoder) FormatDone(_ llm.Usage) ([]protocol.Event, error) {
	if e.usage.TotalTokens == 0 && e.usage.PromptTokens == 0 {
		return nil, nil
	}
	chunk := response{UsageMetadata: &usageMetadata{
		PromptTokenCount:     e.usage.PromptTokens,
		CandidatesTokenCount: e.usage.CompletionTokens,
		TotalTokenCount:      e.usage.TotalTokens,
	}}
	b, _ := json.Marshal(chunk)
	return []protocol.Event{{Data: string(b)}}, nil
}

func (e *streamResponseEncoder) formatDelta(d llm.StreamDelta) *protocol.Event {
	switch v := d.(type) {
	case *llm.TextDelta:
		b, _ := json.Marshal(response{Candidates: []candidate{{
			Content: content{Role: "model", Parts: []part{{Text: v.Text}}},
		}}})
		return &protocol.Event{Data: string(b)}
	case *llm.ThinkingDelta:
		b, _ := json.Marshal(response{Candidates: []candidate{{
			Content: content{Parts: []part{{Text: v.Text, Thought: true}}},
		}}})
		return &protocol.Event{Data: string(b)}
	case *llm.UsageDelta:
		e.usage = v.Usage
		return nil
	case *llm.DoneDelta:
		// Gemini signals completion via finishReason on the final candidate.
		b, _ := json.Marshal(response{Candidates: []candidate{{FinishReason: denormalizeGeminiFinishReason(v.StopReason)}}})
		return &protocol.Event{Data: string(b)}
	}
	return nil
}
