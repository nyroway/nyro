package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
)

type execution struct {
	runtime   *Runtime
	sink      Sink
	runner    *pipeline.Runner
	delivered bool
	stream    streamState
}

type dispatchPhase struct {
	runtime   *Runtime
	execution *execution
}

func (dispatchPhase) Name() string { return "dispatch" }
func (p dispatchPhase) Apply(ctx context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	providerError := p.runtime.dispatch(ctx, p.execution, exchange)
	if providerError == nil {
		return continueOutcome(), nil
	}
	exchange.Error = providerError
	if exchange.Status < 400 {
		exchange.Status = statusOf(providerError, statusBadGateway)
	}
	return pipeline.Outcome{Decision: pipeline.Reject, Error: providerError}, nil
}

type attemptResult struct {
	response  *llm.ChatResponse
	opaque    *protocol.WireResponse
	rawError  *protocol.WireResponse
	err       *llm.Error
	status    int
	latencyMs float64
	retry     bool
	failover  bool
	terminal  bool
}

func (r *Runtime) dispatch(ctx context.Context, execution *execution, exchange *pipeline.Exchange) *llm.Error {
	route := r.snapshot.RouteByModel(exchange.Route.Model)
	if route == nil {
		return llm.ErrorFromStatus(statusServiceUnavailable, "logical route is unavailable")
	}
	targets, strategy := routingTargets(*route)
	ordered := r.router.Select(targets, strategy)
	clientModel := exchange.Request.ModelID()
	for _, target := range ordered {
		upstream := r.snapshot.UpstreamGet(target.UpstreamID)
		if upstream == nil || !upstream.Enabled {
			continue
		}
		actualModel := target.Model
		if actualModel == "" || actualModel == "*" {
			actualModel = clientModel
		}
		egress, err := r.egressFor(exchange.Source, upstream.Protocol)
		if err != nil {
			return llm.ErrorFromStatus(statusInternalServerError, err.Error())
		}
		factory := r.providers.DriverFor(upstream.Provider)
		if factory == nil {
			return llm.ErrorFromStatus(statusInternalServerError, "provider driver is not configured")
		}

		exchange.Target = pipeline.Target{
			ID:           routeTargetID(*route, target),
			UpstreamID:   upstream.ID,
			UpstreamName: upstream.Name,
			Model:        actualModel,
			Endpoint:     egress.Endpoint(),
		}
		attempts := r.settings.MaxRetries
		if attempts < 1 {
			attempts = 1
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return errorFromExecution(err)
			}
			driver := factory()
			if driver == nil {
				return llm.ErrorFromStatus(statusInternalServerError, "provider driver factory returned nil")
			}
			attemptRequest, cloneErr := cloneModelRequest(exchange.Request)
			if cloneErr != nil {
				return llm.ErrorFromStatus(statusInternalServerError, cloneErr.Error())
			}
			attemptRequest.SetModelID(actualModel)
			exchange.Target.UpstreamStatus = nil
			exchange.Target.UpstreamLatencyMs = nil
			result := r.executeAttempt(ctx, execution, driver, *upstream, egress, attemptRequest, exchange)
			if result.status != 0 {
				status := int32(result.status)
				exchange.Target.UpstreamStatus = &status
			}
			latency := int64(result.latencyMs)
			exchange.Target.UpstreamLatencyMs = &latency

			if result.err == nil {
				r.router.Record(routing.KeyOf(target), true, result.latencyMs)
				if result.response != nil {
					exchange.Response = result.response
					exchange.Usage = result.response.Usage
				}
				if result.opaque != nil {
					if err := execution.sink.SendOpaque(ctx, *result.opaque); err != nil {
						return errorFromExecution(fmt.Errorf("send opaque response: %w", err))
					}
					execution.delivered = true
				}
				return nil
			}

			if result.terminal {
				r.router.Record(routing.KeyOf(target), false, result.latencyMs)
				return result.err
			}
			if result.retry {
				if attempt < attempts {
					continue
				}
				r.router.Record(routing.KeyOf(target), false, result.latencyMs)
				break
			}
			if result.failover {
				r.router.Record(routing.KeyOf(target), false, result.latencyMs)
				break
			}

			// A non-retried Provider response is a usable backend response even
			// when it reports a client/provider error. Preserve the existing
			// health semantics and terminate without trying another backend.
			r.router.Record(routing.KeyOf(target), true, result.latencyMs)
			if result.rawError != nil && r.errorPassthroughAllowed(exchange.Source, egress) {
				if err := execution.sink.SendOpaque(ctx, *result.rawError); err != nil {
					return errorFromExecution(fmt.Errorf("send provider error: %w", err))
				}
				execution.delivered = true
			}
			return result.err
		}
	}
	return llm.ErrorFromStatus(statusBadGateway, "all upstream backends failed")
}

func (r *Runtime) executeAttempt(
	ctx context.Context,
	execution *execution,
	driver provider.Driver,
	upstream configsnapshot.Upstream,
	egress protocol.EgressCodec,
	request llm.ModelRequest,
	exchange *pipeline.Exchange,
) attemptResult {
	providerRuntime := runtimeFromUpstream(upstream)
	if err := driver.ExtendRequest(ctx, providerRuntime, request); err != nil {
		return attemptResult{err: llm.ErrorFromStatus(statusBadGateway, "apply provider request extension: "+err.Error()), failover: true}
	}
	wireRequest, err := encodeRequest(egress, request)
	if err != nil {
		return attemptResult{err: llm.ErrorFromStatus(statusBadGateway, "encode request: "+err.Error()), failover: true}
	}
	prepared, err := driver.Prepare(ctx, providerRuntime, wireRequest)
	if err != nil {
		return attemptResult{err: llm.ErrorFromStatus(statusBadGateway, "prepare provider request: "+err.Error()), failover: true}
	}

	started := time.Now()
	response, err := r.transport.Do(ctx, prepared)
	latencyMs := float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return attemptResult{err: errorFromExecution(ctxErr), latencyMs: latencyMs, terminal: true}
		}
		return attemptResult{
			err:       llm.NewError(llm.ErrServiceUnavailable, "provider transport: "+err.Error()),
			latencyMs: latencyMs,
			retry:     true,
		}
	}
	if response == nil {
		return attemptResult{
			err:       llm.NewError(llm.ErrServiceUnavailable, "provider transport returned no response"),
			latencyMs: latencyMs,
			retry:     true,
		}
	}
	defer response.Body.Close()

	classification := driver.Classify(*response)
	if classification.Failed || classification.Error != nil || r.settings.RetryOnStatus[response.StatusCode] {
		wire, readErr := readWireResponse(response)
		if readErr != nil {
			return attemptResult{
				err:       llm.NewError(llm.ErrServiceUnavailable, "read provider error: "+readErr.Error()),
				status:    response.StatusCode,
				latencyMs: latencyMs,
				retry:     true,
			}
		}
		providerError := classification.Error
		if providerError == nil {
			providerError = llm.ErrorFromStatus(uint16(response.StatusCode), "upstream provider request failed")
		}
		providerError = cloneError(providerError)
		if providerError.StatusCode == nil && response.StatusCode > 0 && response.StatusCode <= 65535 {
			status := uint16(response.StatusCode)
			providerError.StatusCode = &status
		}
		if len(providerError.Raw) == 0 {
			providerError.Raw = append(json.RawMessage(nil), wire.Body...)
		}
		if err := driver.ExtendError(ctx, providerRuntime, providerError); err != nil {
			return attemptResult{
				err:       llm.ErrorFromStatus(statusBadGateway, "apply provider error extension: "+err.Error()),
				status:    response.StatusCode,
				latencyMs: latencyMs,
				failover:  true,
			}
		}
		retryable := r.settings.RetryOnStatus[response.StatusCode] ||
			(classification.Retryable && response.StatusCode < 400)
		return attemptResult{
			rawError:  &wire,
			err:       providerError,
			status:    response.StatusCode,
			latencyMs: latencyMs,
			retry:     retryable,
		}
	}

	switch request := request.(type) {
	case *llm.EmbeddingRequest:
		wire, readErr := readWireResponse(response)
		if readErr != nil {
			return attemptResult{err: llm.NewError(llm.ErrServiceUnavailable, "read provider response: "+readErr.Error()), status: response.StatusCode, latencyMs: latencyMs, retry: true}
		}
		if !r.opaquePassthroughAllowed(exchange.Source, egress) {
			return attemptResult{
				err:       llm.ErrorFromStatus(statusBadGateway, "embedding response requires same-endpoint opaque passthrough"),
				status:    response.StatusCode,
				latencyMs: latencyMs,
				failover:  true,
			}
		}
		return attemptResult{opaque: &wire, status: response.StatusCode, latencyMs: latencyMs}
	case *llm.ChatRequest:
		codec, ok := egress.(protocol.ChatEgressCodec)
		if !ok {
			return attemptResult{err: llm.ErrorFromStatus(statusBadGateway, "selected endpoint does not support chat"), status: response.StatusCode, latencyMs: latencyMs, failover: true}
		}
		if request.Stream.Enabled {
			if !egress.Capabilities().Streaming {
				return attemptResult{err: llm.ErrorFromStatus(statusBadGateway, "selected endpoint does not support streaming"), status: response.StatusCode, latencyMs: latencyMs, failover: true}
			}
			streamErr := r.consumeStream(ctx, execution, exchange, response.Body, codec.NewStreamDecoder())
			if streamErr == nil {
				return attemptResult{status: response.StatusCode, latencyMs: latencyMs}
			}
			return attemptResult{
				err:       streamErr,
				status:    response.StatusCode,
				latencyMs: latencyMs,
				retry:     execution.stream.state == streamUncommitted,
				terminal:  execution.stream.state != streamUncommitted,
			}
		}

		wire, readErr := readWireResponse(response)
		if readErr != nil {
			return attemptResult{err: llm.NewError(llm.ErrServiceUnavailable, "read provider response: "+readErr.Error()), status: response.StatusCode, latencyMs: latencyMs, retry: true}
		}
		normalized, decodeErr := codec.DecodeResponse(wire)
		if decodeErr != nil {
			return attemptResult{err: llm.NewError(llm.ErrServiceUnavailable, "decode provider response: "+decodeErr.Error()), status: response.StatusCode, latencyMs: latencyMs, retry: true}
		}
		if normalized == nil {
			return attemptResult{err: llm.NewError(llm.ErrServiceUnavailable, "egress codec returned no response"), status: response.StatusCode, latencyMs: latencyMs, retry: true}
		}
		if err := driver.ExtendResponse(ctx, providerRuntime, normalized); err != nil {
			return attemptResult{err: llm.NewError(llm.ErrServiceUnavailable, "apply provider response extension: "+err.Error()), status: response.StatusCode, latencyMs: latencyMs, retry: true}
		}
		return attemptResult{response: normalized, status: response.StatusCode, latencyMs: latencyMs}
	default:
		return attemptResult{err: llm.ErrorFromStatus(statusInternalServerError, fmt.Sprintf("unsupported LLM request %T", request)), terminal: true}
	}
}

func (r *Runtime) egressFor(source protocol.Endpoint, protocolID string) (protocol.EgressCodec, error) {
	parsed, err := protocol.ParseProtocol(protocolID)
	if err == nil {
		if endpoint, found := r.protocols.EndpointFor(parsed); found {
			if selected, found := r.protocols.Egress(endpoint); found {
				return selected, nil
			}
		}
	}
	if egress, found := r.protocols.Egress(source); found {
		return egress, nil
	}
	return nil, fmt.Errorf("no egress codec for endpoint %s", source)
}

func encodeRequest(codec protocol.EgressCodec, request llm.ModelRequest) (protocol.WireRequest, error) {
	switch request := request.(type) {
	case *llm.ChatRequest:
		chat, ok := codec.(protocol.ChatEgressCodec)
		if !ok {
			return protocol.WireRequest{}, fmt.Errorf("endpoint %s does not support chat", codec.Endpoint())
		}
		return chat.EncodeRequest(request)
	case *llm.EmbeddingRequest:
		embedding, ok := codec.(protocol.EmbeddingEgressCodec)
		if !ok {
			return protocol.WireRequest{}, fmt.Errorf("endpoint %s does not support embedding", codec.Endpoint())
		}
		return embedding.EncodeRequest(request)
	default:
		return protocol.WireRequest{}, fmt.Errorf("unsupported LLM request %T", request)
	}
}

func readWireResponse(response *provider.Response) (protocol.WireResponse, error) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return protocol.WireResponse{}, err
	}
	headers := make(map[string]string, len(response.Headers))
	for key, values := range response.Headers {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return protocol.WireResponse{Status: response.StatusCode, Headers: headers, Body: body}, nil
}

func runtimeFromUpstream(upstream configsnapshot.Upstream) provider.UpstreamRuntime {
	return provider.UpstreamRuntime{
		Name:            upstream.Name,
		Provider:        upstream.Provider,
		Protocol:        upstream.Protocol,
		BaseURL:         upstream.BaseURL,
		CredentialsJSON: upstream.CredentialsJSON,
		ProxyURL:        upstream.ProxyURL,
	}
}

func routingTargets(route configsnapshot.Route) ([]routing.Target, routing.Strategy) {
	targets := make([]routing.Target, 0, len(route.Upstreams))
	for _, target := range route.Upstreams {
		if !target.Enabled {
			continue
		}
		targets = append(targets, routing.Target{
			UpstreamID: target.UpstreamID,
			Model:      target.Model,
			Weight:     target.Weight,
			Priority:   target.Priority,
		})
	}
	return targets, routing.Strategy(route.Balance)
}

func routeTargetID(route configsnapshot.Route, selected routing.Target) string {
	for _, target := range route.Upstreams {
		if target.UpstreamID == selected.UpstreamID &&
			target.Model == selected.Model &&
			target.Weight == selected.Weight &&
			target.Priority == selected.Priority {
			return target.ID
		}
	}
	return ""
}

func (r *Runtime) errorPassthroughAllowed(source protocol.Endpoint, egress protocol.EgressCodec) bool {
	if source != egress.Endpoint() || !egress.Capabilities().ErrorPassthrough {
		return false
	}
	ingress, found := r.protocols.Ingress(source)
	return found && ingress.Capabilities().ErrorPassthrough
}

func (r *Runtime) opaquePassthroughAllowed(source protocol.Endpoint, egress protocol.EgressCodec) bool {
	if source != egress.Endpoint() || !egress.Capabilities().OpaquePassthrough {
		return false
	}
	ingress, found := r.protocols.Ingress(source)
	return found && ingress.Capabilities().OpaquePassthrough
}

func cloneModelRequest(request llm.ModelRequest) (llm.ModelRequest, error) {
	switch request.(type) {
	case *llm.ChatRequest, *llm.EmbeddingRequest:
	default:
		return nil, fmt.Errorf("unsupported LLM request %T", request)
	}
	cloned := deepClone(reflect.ValueOf(request))
	copy, ok := cloned.Interface().(llm.ModelRequest)
	if !ok {
		return nil, fmt.Errorf("clone LLM request %T", request)
	}
	return copy, nil
}

func deepClone(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(deepClone(value.Elem()))
		return clone
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type().Elem())
		clone.Elem().Set(deepClone(value.Elem()))
		return clone
	case reflect.Struct:
		clone := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			clone.Field(index).Set(deepClone(value.Field(index)))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			clone.Index(index).Set(deepClone(value.Index(index)))
		}
		return clone
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			clone.SetMapIndex(deepClone(iterator.Key()), deepClone(iterator.Value()))
		}
		return clone
	default:
		return value
	}
}

func cloneError(source *llm.Error) *llm.Error {
	if source == nil {
		return nil
	}
	clone := *source
	if source.StatusCode != nil {
		status := *source.StatusCode
		clone.StatusCode = &status
	}
	clone.Raw = append(json.RawMessage(nil), source.Raw...)
	return &clone
}
