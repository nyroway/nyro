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
	"github.com/nyroway/nyro/go/internal/llm/provider"
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
	state               streamLifecycle
	failure             failureOrigin
	allowRawErrors      bool
	errorClassification provider.ErrorClassification
	canonicalError      bool
	usage               llm.Usage
	pending             []llm.StreamDelta
	authoritative       bool
}

func (r *Runtime) consumeStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	upstream io.Reader,
	decoder protocol.StreamDecoder,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	allowRawStream bool,
) *llm.Error {
	if upstream == nil {
		return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
			llm.NewError(llm.ErrUnexpectedEOF, "provider stream has no body"))
	}
	if decoder == nil {
		return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
			llm.NewError(llm.ErrStreamMidError, "egress codec returned no stream decoder"))
	}
	reader := bufio.NewReaderSize(upstream, 64*1024)
	var pendingDone *llm.DoneDelta
	for {
		if err := ctx.Err(); err != nil {
			execution.stream.state = streamTerminated
			execution.stream.failure = failureClient
			return errorFromExecution(err)
		}
		line, readErr := reader.ReadString('\n')
		if err := ctx.Err(); err != nil {
			execution.stream.state = streamTerminated
			execution.stream.failure = failureClient
			return errorFromExecution(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" {
				deltas, parseErr := decoder.ParseChunk(payload)
				if parseErr != nil {
					return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
						llm.NewError(llm.ErrStreamMidError, "decode provider stream: "+parseErr.Error()))
				}
				for _, delta := range deltas {
					if done, ok := delta.(*llm.DoneDelta); ok {
						// Done is held until the Provider terminates its wire stream.
						// OpenAI may emit usage after the finish_reason that produced
						// this logical marker but before its [DONE] sentinel.
						pending := *done
						usage := cloneUsage(execution.stream.usage)
						pending.UsageAtDone = &usage
						pendingDone = &pending
						continue
					}
					if providerError := r.acceptDecodedStreamDelta(ctx, execution, exchange, driver, providerRuntime, delta, allowRawStream); providerError != nil {
						return providerError
					}
				}
				if pendingDone != nil && payload == "[DONE]" {
					return r.finishNormalStream(ctx, execution, exchange, decoder, driver, providerRuntime, pendingDone, allowRawStream)
				}
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
				llm.NewError(llm.ErrStreamMidError, "read provider stream: "+readErr.Error()))
		}
		if pendingDone != nil {
			return r.finishNormalStream(ctx, execution, exchange, decoder, driver, providerRuntime, pendingDone, allowRawStream)
		}

		// Finish may flush decoder-local buffered content. A decoder-synthesized
		// Done on EOF cannot turn a truncated provider stream into a normal end.
		for _, delta := range decoder.Finish() {
			if _, done := delta.(*llm.DoneDelta); done {
				continue
			}
			if providerError := r.acceptDecodedStreamDelta(ctx, execution, exchange, driver, providerRuntime, delta, allowRawStream); providerError != nil {
				return providerError
			}
		}
		return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
			llm.NewError(llm.ErrUnexpectedEOF, "provider stream ended unexpectedly"))
	}
}

func (r *Runtime) extendStreamProviderError(
	ctx context.Context,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	providerError *llm.Error,
) (*llm.Error, provider.ErrorClassification, error) {
	providerError = cloneError(providerError)
	classification, err := driver.ExtendError(ctx, providerRuntime, providerError)
	if err != nil {
		return nil, provider.ErrorClassification{}, err
	}
	return providerError, classification, nil
}

func (r *Runtime) failOperationalProviderStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	providerError *llm.Error,
) *llm.Error {
	providerError, _, err := r.extendStreamProviderError(ctx, driver, providerRuntime, providerError)
	if err != nil {
		providerError = llm.ErrorFromStatus(statusBadGateway, "apply provider stream error extension: "+err.Error())
	}
	// Transport/decoder failures keep Runtime's transient and unhealthy
	// defaults; only a canonical StreamErrorDelta owns semantic policy.
	execution.stream.errorClassification = provider.ErrorClassification{Retryable: true, Unhealthy: true}
	execution.stream.canonicalError = false
	return r.failStream(ctx, execution, exchange, providerError)
}

func (r *Runtime) failClassifiedProviderStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	providerError *llm.Error,
) *llm.Error {
	providerError, classification, err := r.extendStreamProviderError(ctx, driver, providerRuntime, providerError)
	if err != nil {
		providerError = llm.ErrorFromStatus(statusBadGateway, "apply provider stream error extension: "+err.Error())
		classification = provider.ErrorClassification{Retryable: true, Unhealthy: true}
		execution.stream.canonicalError = false
	} else {
		execution.stream.canonicalError = true
	}
	execution.stream.errorClassification = classification
	return r.failStream(ctx, execution, exchange, providerError)
}

func (r *Runtime) acceptDecodedStreamDelta(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	delta llm.StreamDelta,
	allowRawStream bool,
) *llm.Error {
	if _, unknown := delta.(*llm.UnknownDelta); unknown && !allowRawStream {
		return nil
	}
	switch terminal := delta.(type) {
	case *llm.StreamErrorDelta:
		providerError := terminal.Error
		if providerError == nil {
			providerError = llm.NewError(llm.ErrStreamMidError, "provider stream failed")
		}
		return r.failClassifiedProviderStream(ctx, execution, exchange, driver, providerRuntime, providerError)
	case *llm.UnexpectedEOFDelta:
		return r.failOperationalProviderStream(ctx, execution, exchange, driver, providerRuntime,
			llm.NewError(llm.ErrUnexpectedEOF, "provider stream ended unexpectedly"))
	}
	return r.acceptStreamDelta(ctx, execution, exchange, delta)
}

func (r *Runtime) finishNormalStream(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	decoder protocol.StreamDecoder,
	driver provider.Driver,
	providerRuntime provider.UpstreamRuntime,
	done *llm.DoneDelta,
	allowRawStream bool,
) *llm.Error {
	// Finish may release decoder-local content that logically precedes Done,
	// such as an unterminated reasoning buffer.
	for _, buffered := range decoder.Finish() {
		if _, terminal := buffered.(*llm.DoneDelta); terminal {
			continue
		}
		if providerError := r.acceptDecodedStreamDelta(ctx, execution, exchange, driver, providerRuntime, buffered, allowRawStream); providerError != nil {
			return providerError
		}
	}
	if providerError := r.acceptStreamDelta(ctx, execution, exchange, done); providerError != nil {
		return providerError
	}
	r.commitStreamAttempt(ctx, execution, exchange)
	return nil
}

func (r *Runtime) acceptStreamDelta(
	ctx context.Context,
	execution *execution,
	exchange *pipeline.Exchange,
	delta llm.StreamDelta,
) *llm.Error {
	if err := r.sendDelta(ctx, execution, exchange, delta); err != nil {
		execution.stream.state = streamTerminated
		if ctxErr := ctx.Err(); ctxErr != nil {
			execution.stream.failure = failureClient
			return errorFromExecution(ctxErr)
		}
		execution.stream.failure = failureDownstream
		return llm.NewError(llm.ErrStreamMidError, "send client stream: "+err.Error())
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		execution.stream.state = streamTerminated
		execution.stream.failure = failureClient
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
	if execution.deliveryClosed {
		return errors.New("stream Sink is closed")
	}
	if usage, ok := delta.(*llm.UsageDelta); ok {
		execution.stream.usage = cloneUsage(usage.Usage)
	}
	if execution.stream.authoritative {
		r.publishStreamDelta(ctx, execution, exchange, delta)
	} else {
		execution.stream.pending = append(execution.stream.pending, delta)
	}
	committed, err := execution.sink.SendDelta(ctx, delta)
	if committed && !execution.stream.authoritative {
		r.commitStreamAttempt(ctx, execution, exchange)
		if execution.stream.state == streamUncommitted {
			execution.stream.state = streamCommitted
		}
	}
	if err != nil {
		execution.deliveryClosed = true
		return err
	}
	if committed {
		execution.delivered = true
	}
	if isTerminalDelta(delta) {
		execution.stream.state = streamTerminated
		execution.deliveryClosed = true
	}
	return nil
}

func (r *Runtime) commitStreamAttempt(ctx context.Context, execution *execution, exchange *pipeline.Exchange) {
	if execution.stream.authoritative {
		return
	}
	execution.stream.authoritative = true
	for _, delta := range execution.stream.pending {
		r.publishStreamDelta(ctx, execution, exchange, delta)
	}
	execution.stream.pending = nil
}

func (r *Runtime) publishStreamDelta(ctx context.Context, execution *execution, exchange *pipeline.Exchange, delta llm.StreamDelta) {
	if usage, ok := delta.(*llm.UsageDelta); ok {
		exchange.Usage = cloneUsage(usage.Usage)
	}
	if execution.runner != nil {
		execution.runner.ObserveDelta(ctx, exchange, delta)
	}
}

func resetStreamAttempt(execution *execution) error {
	if execution.stream.authoritative || execution.stream.state != streamUncommitted {
		return errors.New("cannot reset a committed stream attempt")
	}
	if err := execution.sink.ResetStreamAttempt(); err != nil {
		return err
	}
	execution.stream.pending = nil
	execution.stream.usage = llm.Usage{}
	return nil
}

func (r *Runtime) failStream(ctx context.Context, execution *execution, exchange *pipeline.Exchange, providerError *llm.Error) *llm.Error {
	execution.stream.failure = failureProvider
	if execution.stream.state != streamCommitted {
		return providerError
	}
	deliveredError := providerError
	if !execution.stream.allowRawErrors && len(providerError.Raw) > 0 {
		deliveredError = cloneError(providerError)
		deliveredError.Raw = nil
	}
	terminal := &llm.StreamErrorDelta{Error: deliveredError}
	if err := r.sendDelta(ctx, execution, exchange, terminal); err != nil {
		execution.stream.state = streamTerminated
		return llm.NewError(llm.ErrStreamMidError, fmt.Sprintf("terminate client stream: %v", err))
	}
	return providerError
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
