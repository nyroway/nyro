package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type streamLifecycle uint8

const (
	streamUncommitted streamLifecycle = iota
	streamCommitted
	streamTerminated
)

// streamState is request-scoped authority for the only legal retry boundary:
// a stream remains uncommitted until Sink accepts its first canonical delta.
type streamState struct {
	state streamLifecycle
}

func (r *Runtime) consumeStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	upstream io.Reader,
	decoder protocol.StreamDecoder,
) *llm.Error {
	if upstream == nil {
		return llm.NewError(llm.ErrUnexpectedEOF, "provider stream has no body")
	}
	if decoder == nil {
		return llm.NewError(llm.ErrStreamMidError, "egress codec returned no stream decoder")
	}
	reader := bufio.NewReaderSize(upstream, 64*1024)
	var pendingDone *llm.DoneDelta
	for {
		if err := ctx.Err(); err != nil {
			execution.stream.state = streamTerminated
			return errorFromExecution(err)
		}
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" {
				deltas, parseErr := decoder.ParseChunk(payload)
				if parseErr != nil {
					return r.failStream(ctx, execution, exchange, llm.NewError(llm.ErrStreamMidError, "decode provider stream: "+parseErr.Error()))
				}
				for _, delta := range deltas {
					if done, ok := delta.(*llm.DoneDelta); ok {
						// Done is held until the Provider terminates its wire stream.
						// OpenAI may emit usage after the finish_reason that produced
						// this logical marker but before its [DONE] sentinel.
						pending := *done
						usage := cloneUsage(exchange.Usage)
						pending.UsageAtDone = &usage
						pendingDone = &pending
						continue
					}
					if providerError := r.acceptStreamDelta(ctx, execution, exchange, delta); providerError != nil {
						return providerError
					}
				}
				if pendingDone != nil && payload == "[DONE]" {
					return r.finishNormalStream(ctx, execution, exchange, decoder, pendingDone)
				}
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return r.failStream(ctx, execution, exchange, llm.NewError(llm.ErrStreamMidError, "read provider stream: "+readErr.Error()))
		}
		if pendingDone != nil {
			return r.finishNormalStream(ctx, execution, exchange, decoder, pendingDone)
		}

		// Finish may flush decoder-local buffered content. A decoder-synthesized
		// Done on EOF cannot turn a truncated provider stream into a normal end.
		for _, delta := range decoder.Finish() {
			if _, done := delta.(*llm.DoneDelta); done {
				continue
			}
			if providerError := r.acceptStreamDelta(ctx, execution, exchange, delta); providerError != nil {
				return providerError
			}
		}
		return r.failStream(ctx, execution, exchange, llm.NewError(llm.ErrUnexpectedEOF, "provider stream ended unexpectedly"))
	}
}

func (r *Runtime) finishNormalStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	decoder protocol.StreamDecoder,
	done *llm.DoneDelta,
) *llm.Error {
	// Finish may release decoder-local content that logically precedes Done,
	// such as an unterminated reasoning buffer.
	for _, buffered := range decoder.Finish() {
		if _, terminal := buffered.(*llm.DoneDelta); terminal {
			continue
		}
		if providerError := r.acceptStreamDelta(ctx, execution, exchange, buffered); providerError != nil {
			return providerError
		}
	}
	return r.acceptStreamDelta(ctx, execution, exchange, done)
}

func (r *Runtime) acceptStreamDelta(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	delta llm.StreamDelta,
) *llm.Error {
	providerError := streamDeltaError(delta)
	if providerError != nil && execution.stream.state == streamUncommitted {
		return providerError
	}
	if err := r.sendDelta(ctx, execution, exchange, delta); err != nil {
		execution.stream.state = streamTerminated
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errorFromExecution(ctxErr)
		}
		return llm.NewError(llm.ErrStreamMidError, "send client stream: "+err.Error())
	}
	if providerError != nil {
		return providerError
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		execution.stream.state = streamTerminated
		return errorFromExecution(ctxErr)
	}
	return nil
}

func (r *Runtime) sendDelta(ctx context.Context, execution *execution, exchange *pipeline.Exchange, delta llm.StreamDelta) error {
	if delta == nil {
		return nil
	}
	if execution.stream.state == streamTerminated {
		return errors.New("stream is already terminated")
	}
	if usage, ok := delta.(*llm.UsageDelta); ok {
		exchange.Usage = usage.Usage
	}
	if execution.runner != nil {
		execution.runner.ObserveDelta(ctx, exchange, delta)
	}
	if err := execution.sink.SendDelta(ctx, delta); err != nil {
		return err
	}
	if execution.stream.state == streamUncommitted {
		execution.stream.state = streamCommitted
	}
	execution.delivered = true
	if isTerminalDelta(delta) {
		execution.stream.state = streamTerminated
	}
	return nil
}

func (r *Runtime) failStream(ctx context.Context, execution *execution, exchange *pipeline.Exchange, providerError *llm.Error) *llm.Error {
	if execution.stream.state != streamCommitted {
		return providerError
	}
	terminal := &llm.StreamErrorDelta{Error: providerError}
	if err := r.sendDelta(ctx, execution, exchange, terminal); err != nil {
		execution.stream.state = streamTerminated
		return llm.NewError(llm.ErrStreamMidError, fmt.Sprintf("terminate client stream: %v", err))
	}
	return providerError
}

func streamDeltaError(delta llm.StreamDelta) *llm.Error {
	switch terminal := delta.(type) {
	case *llm.StreamErrorDelta:
		if terminal.Error != nil {
			return terminal.Error
		}
		return llm.NewError(llm.ErrStreamMidError, "provider stream failed")
	case *llm.UnexpectedEOFDelta:
		return llm.NewError(llm.ErrUnexpectedEOF, "provider stream ended unexpectedly")
	default:
		return nil
	}
}

func isTerminalDelta(delta llm.StreamDelta) bool {
	switch delta.(type) {
	case *llm.DoneDelta, *llm.StreamErrorDelta, *llm.UnexpectedEOFDelta:
		return true
	default:
		return false
	}
}

func cloneUsage(source llm.Usage) llm.Usage {
	clone := source
	if source.CacheReadTokens != nil {
		value := *source.CacheReadTokens
		clone.CacheReadTokens = &value
	}
	if source.CacheCreationTokens != nil {
		value := *source.CacheCreationTokens
		clone.CacheCreationTokens = &value
	}
	if source.ServerToolUse != nil {
		value := *source.ServerToolUse
		clone.ServerToolUse = &value
	}
	return clone
}
