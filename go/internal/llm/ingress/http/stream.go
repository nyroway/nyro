package httpingress

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func (sink *httpSink) SendDelta(ctx context.Context, delta llm.StreamDelta) (bool, error) {
	if err := sink.begin(ctx, "HTTP stream Sink"); err != nil {
		return false, err
	}
	if sink.encoder == nil {
		codec, ok := sink.ingress.(protocol.ChatIngressCodec)
		if !ok {
			return false, fmt.Errorf("ingress endpoint %s cannot encode a chat stream", sink.ingress.Endpoint())
		}
		sink.encoder = codec.NewStreamEncoder()
		if sink.encoder == nil {
			return false, fmt.Errorf("ingress endpoint %s returned no stream encoder", sink.ingress.Endpoint())
		}
	}
	if usage, ok := delta.(*llm.UsageDelta); ok {
		sink.usage = usage.Usage
	}
	frames, err := sink.encoder.FormatDeltas([]llm.StreamDelta{delta})
	if err != nil {
		return false, fmt.Errorf("format stream delta: %w", err)
	}
	if _, done := delta.(*llm.DoneDelta); done {
		completion, err := sink.encoder.FormatDone(sink.usage)
		if err != nil {
			return false, fmt.Errorf("format stream completion: %w", err)
		}
		frames = append(frames, completion...)
	}
	terminal := isTerminalDelta(delta)
	if terminal {
		sink.terminated = true
	}
	if len(frames) == 0 {
		return false, contextError(ctx)
	}
	if !sink.streamOpen {
		sink.writer.Header().Set("Content-Type", "text/event-stream")
		sink.writer.Header().Set("Cache-Control", "no-cache")
		sink.writer.Header().Set("Connection", "keep-alive")
		sink.writer.WriteHeader(http.StatusOK)
		sink.streamOpen = true
	}
	if err := writeEvents(ctx, sink.writer, frames); err != nil {
		return false, err
	}
	if err := http.NewResponseController(sink.writer).Flush(); err != nil {
		return false, fmt.Errorf("flush stream events: %w", err)
	}
	return true, contextError(ctx)
}

func writeEvents(ctx context.Context, writer http.ResponseWriter, events []protocol.Event) error {
	for _, event := range events {
		if err := contextError(ctx); err != nil {
			return err
		}
		if _, err := writer.Write(event.Bytes()); err != nil {
			return fmt.Errorf("write stream event: %w", err)
		}
	}
	return nil
}

func isTerminalDelta(delta llm.StreamDelta) bool {
	switch delta.(type) {
	case *llm.DoneDelta, *llm.StreamErrorDelta, *llm.UnexpectedEOFDelta:
		return true
	default:
		return false
	}
}
