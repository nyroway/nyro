package gemini

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// streamResponseDecoder parses Gemini streamGenerateContent chunks. Each chunk
// is a generateContent-response-shaped object; parts map to deltas and
// finishReason/usageMetadata terminate the stream.
type streamResponseDecoder struct {
	done    bool
	stop    string
	started bool
}

func (d *streamResponseDecoder) ParseChunk(payload string) ([]ir.StreamDelta, error) {
	if payload == "" {
		return nil, nil
	}
	var chunk response
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return []ir.StreamDelta{&ir.UnknownDelta{Raw: payload}}, nil
	}
	var out []ir.StreamDelta
	if len(chunk.Candidates) > 0 {
		if !d.started {
			d.started = true
			out = append(out, &ir.MessageStartDelta{Model: chunk.ModelVersion})
		}
		c := chunk.Candidates[0]
		for _, p := range c.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				out = append(out, &ir.ToolCallStartDelta{Index: 0, Name: p.FunctionCall.Name})
			case p.Thought:
				out = append(out, &ir.ThinkingDelta{Text: p.Text})
			default:
				if p.Text != "" {
					out = append(out, &ir.TextDelta{Text: p.Text})
				}
			}
		}
		if c.FinishReason != "" {
			d.stop = normalizeGeminiFinishReason(c.FinishReason)
		}
	}
	if chunk.UsageMetadata != nil {
		out = append(out, &ir.UsageDelta{Usage: ir.Usage{
			PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
			CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
		}})
	}
	if d.stop != "" && !d.done {
		d.done = true
		out = append(out, &ir.DoneDelta{StopReason: d.stop})
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

// streamResponseEncoder formats IR deltas as Gemini streamGenerateContent
// chunks (SSE data frames, one response object per delta).
type streamResponseEncoder struct {
	usage ir.Usage
}

func (e *streamResponseEncoder) FormatDeltas(deltas []ir.StreamDelta) ([]codec.SSE, error) {
	var out []codec.SSE
	for _, d := range deltas {
		if f := e.formatDelta(d); f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

// FormatDone emits a final usageMetadata chunk if usage was captured (Gemini
// has no [DONE] terminator).
func (e *streamResponseEncoder) FormatDone(_ ir.Usage) ([]codec.SSE, error) {
	if e.usage.TotalTokens == 0 && e.usage.PromptTokens == 0 {
		return nil, nil
	}
	chunk := response{UsageMetadata: &usageMetadata{
		PromptTokenCount:     e.usage.PromptTokens,
		CandidatesTokenCount: e.usage.CompletionTokens,
		TotalTokenCount:      e.usage.TotalTokens,
	}}
	b, _ := json.Marshal(chunk)
	return []codec.SSE{{Data: string(b)}}, nil
}

func (e *streamResponseEncoder) formatDelta(d ir.StreamDelta) *codec.SSE {
	switch v := d.(type) {
	case *ir.TextDelta:
		b, _ := json.Marshal(response{Candidates: []candidate{{
			Content: content{Role: "model", Parts: []part{{Text: v.Text}}},
		}}})
		return &codec.SSE{Data: string(b)}
	case *ir.ThinkingDelta:
		b, _ := json.Marshal(response{Candidates: []candidate{{
			Content: content{Parts: []part{{Text: v.Text, Thought: true}}},
		}}})
		return &codec.SSE{Data: string(b)}
	case *ir.UsageDelta:
		e.usage = v.Usage
		return nil
	case *ir.DoneDelta:
		// Gemini signals completion via finishReason on the final candidate.
		b, _ := json.Marshal(response{Candidates: []candidate{{FinishReason: v.StopReason}}})
		return &codec.SSE{Data: string(b)}
	}
	return nil
}
