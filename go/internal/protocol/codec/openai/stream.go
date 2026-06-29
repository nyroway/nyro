package openai

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// streamResponseDecoder implements codec.StreamResponseDecoder for OpenAI
// chat-completion SSE streams. One instance per request stream; it accumulates
// finish_reason and de-dupes the terminal Done.
type streamResponseDecoder struct {
	id      string
	model   string
	started bool
	done    bool
	stop    string
	think   thinkState
}

func (d *streamResponseDecoder) ParseChunk(payload string) ([]ir.StreamDelta, error) {
	if payload == "[DONE]" {
		if d.done {
			return nil, nil
		}
		d.done = true
		return []ir.StreamDelta{&ir.DoneDelta{StopReason: d.stop}}, nil
	}

	var w chatCompletionChunk
	if err := json.Unmarshal([]byte(payload), &w); err != nil {
		// Keep-alive (":") or unparseable line — preserve verbatim.
		return []ir.StreamDelta{&ir.UnknownDelta{Raw: payload}}, nil
	}

	var out []ir.StreamDelta
	if !d.started && (w.ID != "" || w.Model != "") {
		d.id, d.model, d.started = w.ID, w.Model, true
		out = append(out, &ir.MessageStartDelta{ID: w.ID, Model: w.Model})
	}
	for _, c := range w.Choices {
		if c.Delta.Content != "" {
			out = append(out, processThinkTags(c.Delta.Content, &d.think)...)
		}
		if c.Delta.ReasoningContent != "" {
			out = append(out, &ir.ThinkingDelta{Text: c.Delta.ReasoningContent})
		} else if c.Delta.Reasoning != "" {
			out = append(out, &ir.ThinkingDelta{Text: c.Delta.Reasoning})
		}
		for _, tc := range c.Delta.ToolCalls {
			if tc.ID != "" || tc.Function.Name != "" {
				out = append(out, &ir.ToolCallStartDelta{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name})
			}
			if tc.Function.Arguments != "" {
				out = append(out, &ir.ToolCallDeltaDelta{Index: tc.Index, Arguments: tc.Function.Arguments})
			}
		}
		if c.FinishReason != nil {
			d.stop = *c.FinishReason
		}
	}
	if w.Usage != nil {
		out = append(out, &ir.UsageDelta{Usage: usageFromWire(*w.Usage)})
	}
	// Emit Done as soon as a finish_reason arrives (robust even if the
	// provider omits the [DONE] sentinel); [DONE] then becomes a no-op.
	if d.stop != "" && !d.done {
		d.done = true
		out = append(out, &ir.DoneDelta{StopReason: d.stop})
	}
	return out, nil
}

func (d *streamResponseDecoder) Finish() []ir.StreamDelta {
	out := flushThink(&d.think)
	if d.done {
		return out
	}
	d.done = true
	return append(out, &ir.DoneDelta{StopReason: d.stop})
}

// streamResponseEncoder implements codec.StreamResponseEncoder for OpenAI
// chat-completion SSE streams.
type streamResponseEncoder struct {
	id      string
	model   string
	created int64
}

func (e *streamResponseEncoder) FormatDeltas(deltas []ir.StreamDelta) ([]codec.SSE, error) {
	var out []codec.SSE
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

func (e *streamResponseEncoder) FormatDone(usage ir.Usage) ([]codec.SSE, error) {
	var out []codec.SSE
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 {
		c := e.baseChunk()
		u := usageToWire(usage)
		c.Usage = &u
		out = append(out, e.chunk(c))
	}
	out = append(out, codec.SSE{Data: "[DONE]"})
	return out, nil
}

func (e *streamResponseEncoder) formatDelta(d ir.StreamDelta) []codec.SSE {
	switch v := d.(type) {
	case *ir.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.created = nowUnix()
		c := chatCompletionChunk{
			ID: v.ID, Object: "chat.completion.chunk", Created: e.created, Model: v.Model,
			Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{Role: "assistant"}}},
		}
		return []codec.SSE{e.chunk(c)}
	case *ir.TextDelta:
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{Content: v.Text}}}
		return []codec.SSE{e.chunk(c)}
	case *ir.ThinkingDelta:
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ReasoningContent: v.Text}}}
		return []codec.SSE{e.chunk(c)}
	case *ir.ToolCallStartDelta:
		tc := chatDeltaToolCall{Index: v.Index, ID: v.ID, Type: "function", Function: chatToolCallFn{Name: v.Name}}
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ToolCalls: []chatDeltaToolCall{tc}}}}
		return []codec.SSE{e.chunk(c)}
	case *ir.ToolCallDeltaDelta:
		tc := chatDeltaToolCall{Index: v.Index, Function: chatToolCallFn{Arguments: v.Arguments}}
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ToolCalls: []chatDeltaToolCall{tc}}}}
		return []codec.SSE{e.chunk(c)}
	case *ir.DoneDelta:
		fr := toOpenAIFinishReason(v.StopReason)
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, FinishReason: &fr}}
		return []codec.SSE{e.chunk(c)}
	}
	return nil
}

func (e *streamResponseEncoder) baseChunk() chatCompletionChunk {
	return chatCompletionChunk{ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model}
}

func (e *streamResponseEncoder) chunk(c chatCompletionChunk) codec.SSE {
	b, _ := json.Marshal(c)
	return codec.SSE{Data: string(b)}
}
