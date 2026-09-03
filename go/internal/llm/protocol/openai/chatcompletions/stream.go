package chatcompletions

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// streamResponseDecoder implements protocol.StreamResponseDecoder for OpenAI
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

func (d *streamResponseDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	if payload == "[DONE]" {
		if d.done {
			return nil, nil
		}
		d.done = true
		return []llm.StreamDelta{&llm.DoneDelta{StopReason: d.stop}}, nil
	}
	var errorEnvelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &errorEnvelope) == nil && len(errorEnvelope.Error) > 0 && string(errorEnvelope.Error) != "null" {
		providerError, _ := (egress{}).DecodeError(protocol.WireResponse{Body: []byte(payload)})
		d.done = true
		return []llm.StreamDelta{&llm.StreamErrorDelta{Error: providerError}}, nil
	}

	var w chatCompletionChunk
	if err := json.Unmarshal([]byte(payload), &w); err != nil {
		// Keep-alive (":") or unparseable line — preserve verbatim.
		return []llm.StreamDelta{&llm.UnknownDelta{Raw: payload}}, nil
	}

	var out []llm.StreamDelta
	if !d.started && (w.ID != "" || w.Model != "") {
		d.id, d.model, d.started = w.ID, w.Model, true
		out = append(out, &llm.MessageStartDelta{ID: w.ID, Model: w.Model})
	}
	for _, c := range w.Choices {
		if c.Delta.Content != "" {
			out = append(out, processThinkTags(c.Delta.Content, &d.think)...)
		}
		if c.Delta.ReasoningContent != "" {
			out = append(out, &llm.ThinkingDelta{Text: c.Delta.ReasoningContent})
		} else if c.Delta.Reasoning != "" {
			out = append(out, &llm.ThinkingDelta{Text: c.Delta.Reasoning})
		}
		for _, tc := range c.Delta.ToolCalls {
			if tc.ID != "" || tc.Function.Name != "" {
				out = append(out, &llm.ToolCallStartDelta{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name})
			}
			if tc.Function.Arguments != "" {
				out = append(out, &llm.ToolCallDeltaDelta{Index: tc.Index, Arguments: tc.Function.Arguments})
			}
		}
		if c.FinishReason != nil {
			d.stop = *c.FinishReason
		}
	}
	if w.Usage != nil {
		out = append(out, &llm.UsageDelta{Usage: usageFromWire(*w.Usage)})
	}
	// Emit Done as soon as a finish_reason arrives (robust even if the
	// provider omits the [DONE] sentinel); [DONE] then becomes a no-op.
	if d.stop != "" && !d.done {
		d.done = true
		out = append(out, &llm.DoneDelta{StopReason: d.stop})
	}
	return out, nil
}

func (d *streamResponseDecoder) Finish() []llm.StreamDelta {
	out := flushThink(&d.think)
	if d.done {
		return out
	}
	d.done = true
	return append(out, &llm.DoneDelta{StopReason: d.stop})
}

// streamResponseEncoder implements protocol.StreamResponseEncoder for OpenAI
// chat-completion SSE streams.
type streamResponseEncoder struct {
	id      string
	model   string
	created int64
}

func (e *streamResponseEncoder) FormatDeltas(deltas []llm.StreamDelta) ([]protocol.Event, error) {
	var out []protocol.Event
	for _, d := range deltas {
		out = append(out, e.formatDelta(d)...)
	}
	return out, nil
}

func (e *streamResponseEncoder) FormatDone(usage llm.Usage) ([]protocol.Event, error) {
	var out []protocol.Event
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 {
		c := e.baseChunk()
		u := usageToWire(usage)
		c.Usage = &u
		out = append(out, e.chunk(c))
	}
	out = append(out, protocol.Event{Data: "[DONE]"})
	return out, nil
}

func (e *streamResponseEncoder) formatDelta(d llm.StreamDelta) []protocol.Event {
	switch v := d.(type) {
	case *llm.MessageStartDelta:
		e.id, e.model = v.ID, v.Model
		e.created = nowUnix()
		c := chatCompletionChunk{
			ID: v.ID, Object: "chat.completion.chunk", Created: e.created, Model: v.Model,
			Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{Role: "assistant"}}},
		}
		return []protocol.Event{e.chunk(c)}
	case *llm.TextDelta:
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{Content: v.Text}}}
		return []protocol.Event{e.chunk(c)}
	case *llm.ThinkingDelta:
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ReasoningContent: v.Text}}}
		return []protocol.Event{e.chunk(c)}
	case *llm.ToolCallStartDelta:
		tc := chatDeltaToolCall{Index: v.Index, ID: v.ID, Type: "function", Function: chatToolCallFn{Name: v.Name}}
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ToolCalls: []chatDeltaToolCall{tc}}}}
		return []protocol.Event{e.chunk(c)}
	case *llm.ToolCallDeltaDelta:
		tc := chatDeltaToolCall{Index: v.Index, Function: chatToolCallFn{Arguments: v.Arguments}}
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, Delta: chatDelta{ToolCalls: []chatDeltaToolCall{tc}}}}
		return []protocol.Event{e.chunk(c)}
	case *llm.DoneDelta:
		fr := toOpenAIFinishReason(v.StopReason)
		c := e.baseChunk()
		c.Choices = []chatChunkChoice{{Index: 0, FinishReason: &fr}}
		return []protocol.Event{e.chunk(c)}
	case *llm.StreamErrorDelta:
		body, _ := encodeErrorBody(v.Error)
		return []protocol.Event{{Data: string(body)}}
	case *llm.UnexpectedEOFDelta:
		body, _ := encodeErrorBody(llm.NewError(llm.ErrUnexpectedEOF, "provider stream ended unexpectedly"))
		return []protocol.Event{{Data: string(body)}}
	}
	return nil
}

func (e *streamResponseEncoder) baseChunk() chatCompletionChunk {
	return chatCompletionChunk{ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model}
}

func (e *streamResponseEncoder) chunk(c chatCompletionChunk) protocol.Event {
	b, _ := json.Marshal(c)
	return protocol.Event{Data: string(b)}
}
