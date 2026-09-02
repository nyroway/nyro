package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	anthropicmessages "github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	openairesponses "github.com/nyroway/nyro/go/internal/llm/protocol/openai/responses"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
)

type scriptedStreamDecoder struct{}

func (*scriptedStreamDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	switch payload {
	case "start":
		return []llm.StreamDelta{&llm.TextDelta{Text: "hello"}}, nil
	case "usage":
		return []llm.StreamDelta{&llm.UsageDelta{Usage: llm.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}}}, nil
	case "done":
		return []llm.StreamDelta{&llm.DoneDelta{StopReason: "stop"}}, nil
	case "bad":
		return nil, errors.New("malformed provider stream")
	default:
		return nil, nil
	}
}

func (*scriptedStreamDecoder) Finish() []llm.StreamDelta { return nil }

type bufferedFinishDecoder struct{}

func (*bufferedFinishDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	if payload == "done" {
		return []llm.StreamDelta{&llm.DoneDelta{StopReason: "stop"}}, nil
	}
	return nil, nil
}

func (*bufferedFinishDecoder) Finish() []llm.StreamDelta {
	return []llm.StreamDelta{&llm.TextDelta{Text: "buffered"}}
}

type trailingUsageDecoder struct{}

func (*trailingUsageDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	switch payload {
	case "done":
		return []llm.StreamDelta{&llm.DoneDelta{StopReason: "stop"}}, nil
	case "usage":
		return []llm.StreamDelta{&llm.UsageDelta{Usage: llm.Usage{TotalTokens: 7}}}, nil
	default:
		return nil, nil
	}
}

func (*trailingUsageDecoder) Finish() []llm.StreamDelta { return nil }

type providerErrorStreamDecoder struct{}

func (*providerErrorStreamDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	switch payload {
	case "start":
		return []llm.StreamDelta{&llm.TextDelta{Text: "hello"}}, nil
	case "error":
		return []llm.StreamDelta{&llm.StreamErrorDelta{Error: llm.NewError(llm.ErrRateLimitError, "decoded stream error")}}, nil
	default:
		return nil, nil
	}
}

func (*providerErrorStreamDecoder) Finish() []llm.StreamDelta { return nil }

type rawProviderErrorStreamDecoder struct{}

func (*rawProviderErrorStreamDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	switch payload {
	case "start":
		return []llm.StreamDelta{&llm.TextDelta{Text: "hello"}}, nil
	case "error":
		return []llm.StreamDelta{&llm.StreamErrorDelta{Error: llm.NewError(llm.ErrRateLimitError, "provider stream error").WithRaw(json.RawMessage(`{"secret":true}`))}}, nil
	default:
		return nil, nil
	}
}

func (*rawProviderErrorStreamDecoder) Finish() []llm.StreamDelta { return nil }

type rawStreamDecoder struct{}

func (*rawStreamDecoder) ParseChunk(payload string) ([]llm.StreamDelta, error) {
	switch payload {
	case "start":
		return []llm.StreamDelta{&llm.TextDelta{Text: "hello"}}, nil
	case "raw":
		return []llm.StreamDelta{&llm.UnknownDelta{Raw: `{"provider_secret":true}`}}, nil
	case "done":
		return []llm.StreamDelta{&llm.DoneDelta{StopReason: "stop"}}, nil
	default:
		return nil, nil
	}
}

func (*rawStreamDecoder) Finish() []llm.StreamDelta { return nil }

func TestExecuteAllowsUnknownDeltaOnlyForSameEndpointNegotiation(t *testing.T) {
	tests := []struct {
		name        string
		target      protocol.Endpoint
		ingressCaps protocol.Capabilities
		egressCaps  protocol.Capabilities
		wantRaw     bool
	}{
		{
			name:        "same Endpoint negotiated",
			target:      protocol.OpenAIChatCompletionsV1,
			ingressCaps: protocol.Capabilities{Streaming: true, RawStreamPassthrough: true},
			egressCaps:  protocol.Capabilities{Streaming: true, RawStreamPassthrough: true},
			wantRaw:     true,
		},
		{
			name:        "cross Endpoint",
			target:      protocol.AnthropicMessagesV1,
			ingressCaps: protocol.Capabilities{Streaming: true, RawStreamPassthrough: true},
			egressCaps:  protocol.Capabilities{Streaming: true, RawStreamPassthrough: true},
		},
		{
			name:        "same Endpoint without ingress negotiation",
			target:      protocol.OpenAIChatCompletionsV1,
			ingressCaps: protocol.Capabilities{Streaming: true},
			egressCaps:  protocol.Capabilities{Streaming: true, RawStreamPassthrough: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntimeFixture(t, runtimeFixture{
				routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
				upstreams: []configsnapshot.Upstream{upstream("backend", "test", test.target.Protocol.String())},
				settings:  map[string]string{"proxy.max_retries": "1"},
				ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1, caps: test.ingressCaps}},
				egress: []protocol.EgressCodec{testChatEgress{
					endpoint: test.target,
					caps:     test.egressCaps,
					state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
						return &rawStreamDecoder{}
					}},
				}},
				providers: []provider.Registration{providerRegistration("test", &testDriver{})},
				transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
					return response(200, "data: start\n\ndata: raw\n\ndata: done\n\n"), nil
				}),
			})
			request := llm.NewChatRequest("client-model", nil)
			request.Stream.Enabled = true
			sink := &recordingSink{}

			completion := runtime.Execute(context.Background(), Call{
				Request: request,
				Source:  protocol.OpenAIChatCompletionsV1,
				Sink:    sink,
			})

			var raw []*llm.UnknownDelta
			for _, delta := range sink.deltas {
				if unknown, ok := delta.(*llm.UnknownDelta); ok {
					raw = append(raw, unknown)
				}
			}
			if test.wantRaw {
				if completion.Error != nil || len(raw) != 1 || raw[0].Raw != `{"provider_secret":true}` {
					t.Fatalf("completion/raw = %+v/%+v", completion, raw)
				}
				return
			}
			if len(raw) != 0 {
				t.Fatalf("unnegotiated raw deltas = %+v", raw)
			}
			if completion.Error != nil {
				t.Fatalf("completion error = %+v, want unknown delta suppressed", completion.Error)
			}
			if _, ok := sink.deltas[len(sink.deltas)-1].(*llm.DoneDelta); !ok {
				t.Fatalf("terminal delta = %#v", sink.deltas[len(sink.deltas)-1])
			}
		})
	}
}

func TestExecuteExtendsDecodedStreamErrorBeforeDelivery(t *testing.T) {
	var extensions atomic.Int32
	driver := &testDriver{extendError: func(_ context.Context, _ provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
		extensions.Add(1)
		if providerError.Kind != llm.ErrRateLimitError || providerError.Message != "decoded stream error" {
			t.Fatalf("decoded stream error before extension = %+v", providerError)
		}
		providerError.Message = "provider: " + providerError.Message
		return provider.ErrorClassification{}, nil
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true},
			state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
				return &providerErrorStreamDecoder{}
			}},
		}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			return response(200, "data: start\n\ndata: error\n\n"), nil
		}),
	})
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := extensions.Load(); got != 1 {
		t.Fatalf("Driver error extensions = %d, want 1", got)
	}
	if completion.Error == nil || completion.Error.Message != "provider: decoded stream error" {
		t.Fatalf("completion error = %+v", completion.Error)
	}
	if len(sink.deltas) != 2 {
		t.Fatalf("sink deltas = %#v", sink.deltas)
	}
	terminal, ok := sink.deltas[1].(*llm.StreamErrorDelta)
	if !ok || terminal.Error == nil || terminal.Error.Message != "provider: decoded stream error" {
		t.Fatalf("terminal delta = %#v", sink.deltas[1])
	}
}

func TestExecuteExtendsUnexpectedEOFProviderErrorBeforeDelivery(t *testing.T) {
	var extensions atomic.Int32
	driver := &testDriver{extendError: func(_ context.Context, _ provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
		extensions.Add(1)
		providerError.Message = "provider: " + providerError.Message
		return provider.ErrorClassification{}, nil
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true},
			state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
				return &scriptedStreamDecoder{}
			}},
		}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "data: start\n\n"), nil
		}),
	})
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := extensions.Load(); got != 1 {
		t.Fatalf("Driver error extensions = %d, want 1", got)
	}
	if completion.Error == nil || !strings.HasPrefix(completion.Error.Message, "provider: ") {
		t.Fatalf("completion error = %+v", completion.Error)
	}
	terminal, ok := sink.deltas[len(sink.deltas)-1].(*llm.StreamErrorDelta)
	if !ok || terminal.Error == nil || !strings.HasPrefix(terminal.Error.Message, "provider: ") {
		t.Fatalf("terminal delta = %#v", sink.deltas[len(sink.deltas)-1])
	}
}

func TestExecuteSanitizesCrossEndpointStreamErrorRaw(t *testing.T) {
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolAnthropicMessages)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress: []protocol.IngressCodec{testIngress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true, ErrorPassthrough: true},
		}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.AnthropicMessagesV1,
			caps:     protocol.Capabilities{Streaming: true, ErrorPassthrough: true},
			state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
				return &rawProviderErrorStreamDecoder{}
			}},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "data: start\n\ndata: error\n\n"), nil
		}),
	})
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if completion.Error == nil || string(completion.Error.Raw) != `{"secret":true}` {
		t.Fatalf("diagnostic completion error = %+v", completion.Error)
	}
	terminal, ok := sink.deltas[len(sink.deltas)-1].(*llm.StreamErrorDelta)
	if !ok || terminal.Error == nil || len(terminal.Error.Raw) != 0 {
		t.Fatalf("cross-Endpoint terminal delta = %#v", sink.deltas[len(sink.deltas)-1])
	}
}

func TestExecuteDecodesBuiltInProviderStreamErrorEvents(t *testing.T) {
	tests := []struct {
		name         string
		egress       protocol.EgressCodec
		startPayload string
		errorPayload string
		wantKind     llm.ErrorKind
		wantMessage  string
	}{
		{
			name:         "OpenAI Responses response.failed",
			egress:       openairesponses.NewEgress(),
			startPayload: `{"type":"response.created","response":{"id":"r1","model":"provider-model","status":"in_progress"}}`,
			errorPayload: `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"provider crashed"}}}`,
			wantKind:     llm.ErrServerError,
			wantMessage:  "provider crashed",
		},
		{
			name:         "Anthropic error",
			egress:       anthropicmessages.NewEgress(),
			startPayload: `{"type":"message_start","message":{"id":"m1","model":"provider-model","usage":{"input_tokens":1}}}`,
			errorPayload: `{"type":"error","error":{"type":"overloaded_error","message":"capacity exhausted"}}`,
			wantKind:     llm.ErrServiceUnavailable,
			wantMessage:  "capacity exhausted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntimeFixture(t, runtimeFixture{
				routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "provider-model", 1))},
				upstreams: []configsnapshot.Upstream{upstream("backend", "test", test.egress.Endpoint().Protocol.String())},
				settings:  map[string]string{"proxy.max_retries": "1"},
				ingress: []protocol.IngressCodec{testIngress{
					endpoint: protocol.OpenAIChatCompletionsV1,
					caps:     protocol.Capabilities{Streaming: true, ErrorPassthrough: true},
				}},
				egress:    []protocol.EgressCodec{test.egress},
				providers: []provider.Registration{providerRegistration("test", &testDriver{})},
				transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
					return response(200, "data: "+test.startPayload+"\n\ndata: "+test.errorPayload+"\n\n"), nil
				}),
			})
			request := llm.NewChatRequest("client-model", []llm.Message{{
				Role: llm.RoleUser, Content: &llm.TextContent{Text: "hello"},
			}})
			request.Stream.Enabled = true
			sink := &recordingSink{}

			completion := runtime.Execute(context.Background(), Call{
				Request: request,
				Source:  protocol.OpenAIChatCompletionsV1,
				Sink:    sink,
			})

			if completion.Error == nil || completion.Error.Kind != test.wantKind || completion.Error.Message != test.wantMessage || string(completion.Error.Raw) != test.errorPayload {
				t.Fatalf("completion error = %+v", completion.Error)
			}
			terminal, ok := sink.deltas[len(sink.deltas)-1].(*llm.StreamErrorDelta)
			if !ok || terminal.Error == nil || terminal.Error.Kind != test.wantKind || terminal.Error.Message != test.wantMessage || len(terminal.Error.Raw) != 0 {
				t.Fatalf("cross-Endpoint terminal delta = %#v", sink.deltas[len(sink.deltas)-1])
			}
		})
	}
}

func TestExecuteNeverCallsSinkAfterFirstStreamWriteFailure(t *testing.T) {
	runtime := runtimeWithStreamDecoder(t, func() protocol.StreamDecoder {
		return &scriptedStreamDecoder{}
	}, "data: start\n\ndata: done\n\n")
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	var deltaAttempts atomic.Int32
	sink := &recordingSink{onDelta: func(llm.StreamDelta) error {
		deltaAttempts.Add(1)
		return errors.New("client stream write failed")
	}}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if completion.Error == nil || !strings.Contains(completion.Error.Message, "client stream write failed") {
		t.Fatalf("completion error = %+v", completion.Error)
	}
	if got := deltaAttempts.Load(); got != 1 {
		t.Fatalf("Sink delta attempts = %d, want 1", got)
	}
	if len(sink.errors) != 0 || len(sink.responses) != 0 || len(sink.opaque) != 0 {
		t.Fatalf("Sink was called after terminal stream failure: errors/responses/opaque = %d/%d/%d", len(sink.errors), len(sink.responses), len(sink.opaque))
	}
}

func TestExecuteKeepsProviderHealthNeutralOnDownstreamStreamFailure(t *testing.T) {
	for _, initiallyHealthy := range []bool{true, false} {
		t.Run(fmt.Sprintf("initially_healthy_%t", initiallyHealthy), func(t *testing.T) {
			router := routing.New()
			target := routeTarget("target", "backend", "served-model", 1)
			key := routing.KeyOf(routing.Target{UpstreamID: target.UpstreamID, Model: target.Model})
			if !initiallyHealthy {
				router.Record(key, false, 0)
			}
			runtime := streamRuntimeWithRouter(t, router, transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
				return response(200, "data: start\n\ndata: done\n\n"), nil
			}))
			request := llm.NewChatRequest("client-model", nil)
			request.Stream.Enabled = true
			sink := &recordingSink{onDelta: func(llm.StreamDelta) error {
				return errors.New("downstream closed")
			}}

			completion := runtime.Execute(context.Background(), Call{
				Request: request,
				Source:  protocol.OpenAIChatCompletionsV1,
				Sink:    sink,
			})

			if completion.Error == nil || !strings.Contains(completion.Error.Message, "downstream closed") {
				t.Fatalf("completion error = %+v", completion.Error)
			}
			if got := router.IsHealthy(key); got != initiallyHealthy {
				t.Fatalf("Provider health after downstream failure = %t, want unchanged %t", got, initiallyHealthy)
			}
		})
	}
}

type blockingCancelBody struct {
	ctx     context.Context
	started chan struct{}
}

func (body *blockingCancelBody) Read([]byte) (int, error) {
	close(body.started)
	<-body.ctx.Done()
	return 0, errors.New("transport read interrupted")
}

func (*blockingCancelBody) Close() error { return nil }

func TestExecuteRechecksCancellationAfterBlockedReadAndKeepsHealthNeutral(t *testing.T) {
	for _, initiallyHealthy := range []bool{true, false} {
		t.Run(fmt.Sprintf("initially_healthy_%t", initiallyHealthy), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			router := routing.New()
			target := routeTarget("target", "backend", "served-model", 1)
			key := routing.KeyOf(routing.Target{UpstreamID: target.UpstreamID, Model: target.Model})
			if !initiallyHealthy {
				router.Record(key, false, 0)
			}
			runtime := streamRuntimeWithRouter(t, router, transportFunc(func(callCtx context.Context, _ provider.Request) (*provider.Response, error) {
				return &provider.Response{StatusCode: 200, Body: &blockingCancelBody{ctx: callCtx, started: started}}, nil
			}))
			request := llm.NewChatRequest("client-model", nil)
			request.Stream.Enabled = true
			go func() {
				<-started
				cancel()
			}()

			completion := runtime.Execute(ctx, Call{
				Request: request,
				Source:  protocol.OpenAIChatCompletionsV1,
				Sink:    &recordingSink{},
			})

			if completion.Error == nil || completion.Error.Message != "request canceled" {
				t.Fatalf("completion error = %+v, want request canceled", completion.Error)
			}
			if got := router.IsHealthy(key); got != initiallyHealthy {
				t.Fatalf("Provider health after cancellation = %t, want unchanged %t", got, initiallyHealthy)
			}
		})
	}
}

func streamRuntimeWithRouter(t *testing.T, router *routing.Router, transport provider.Transport) *Runtime {
	t.Helper()
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true},
			state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
				return &scriptedStreamDecoder{}
			}},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transport,
		router:    router,
	})
}

func TestExecuteRetainsTrailingUsageBeforeNormalStreamTermination(t *testing.T) {
	runtime := runtimeWithStreamDecoder(t, func() protocol.StreamDecoder {
		return &trailingUsageDecoder{}
	}, "data: done\n\ndata: usage\n\n")
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if completion.Error != nil || completion.Usage.TotalTokens != 7 {
		t.Fatalf("completion = %+v, want trailing usage", completion)
	}
	if len(sink.deltas) != 2 {
		t.Fatalf("sink deltas = %#v, want Usage then Done", sink.deltas)
	}
	if _, ok := sink.deltas[0].(*llm.UsageDelta); !ok {
		t.Fatalf("first delta = %#v, want Usage", sink.deltas[0])
	}
	done, ok := sink.deltas[1].(*llm.DoneDelta)
	if !ok {
		t.Fatalf("terminal delta = %#v, want Done", sink.deltas[1])
	}
	if done.UsageAtDone == nil || done.UsageAtDone.TotalTokens != 0 {
		t.Fatalf("Done usage snapshot = %#v, want pre-trailing zero usage", done.UsageAtDone)
	}
}

func TestExecuteFlushesDecoderBeforeNormalStreamTermination(t *testing.T) {
	runtime := runtimeWithStreamDecoder(t, func() protocol.StreamDecoder {
		return &bufferedFinishDecoder{}
	}, "data: done\n\n")
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if completion.Error != nil {
		t.Fatalf("completion error = %v", completion.Error)
	}
	if len(sink.deltas) != 2 {
		t.Fatalf("sink deltas = %#v, want buffered content plus Done", sink.deltas)
	}
	if text, ok := sink.deltas[0].(*llm.TextDelta); !ok || text.Text != "buffered" {
		t.Fatalf("first delta = %#v, want buffered content", sink.deltas[0])
	}
	if _, ok := sink.deltas[1].(*llm.DoneDelta); !ok {
		t.Fatalf("terminal delta = %#v, want Done", sink.deltas[1])
	}
}

func runtimeWithStreamDecoder(t *testing.T, newDecoder func() protocol.StreamDecoder, body string) *Runtime {
	t.Helper()
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true},
			state:    &codecState{newStreamDecoder: newDecoder},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			return response(200, body), nil
		}),
	})
}

func TestExecuteCanFailOverBeforeFirstStreamDelta(t *testing.T) {
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	runtime := streamRuntime(t,
		[]configsnapshot.RouteTarget{
			routeTarget("first-target", "first", "first-model", 1),
			routeTarget("second-target", "second", "second-model", 2),
		},
		[]configsnapshot.Upstream{
			upstream("first", "test", provider.ProtocolOpenAIChatCompletions),
			upstream("second", "test", provider.ProtocolOpenAIChatCompletions),
		},
		transportFunc(func(_ context.Context, request provider.Request) (*provider.Response, error) {
			if request.URL == "https://first.example/invoke" {
				firstCalls.Add(1)
				return response(200, "data: bad\n\n"), nil
			}
			secondCalls.Add(1)
			return response(200, "data: start\n\ndata: usage\n\ndata: done\n\n"), nil
		}), nil,
	)
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("first backend calls = %d, want 1", got)
	}
	if got := secondCalls.Load(); got != 1 {
		t.Fatalf("second backend calls = %d, want 1", got)
	}
	if completion.Error != nil || !completion.Streamed || completion.Usage.TotalTokens != 5 {
		t.Fatalf("completion = %+v", completion)
	}
	if len(sink.deltas) != 3 {
		t.Fatalf("sink deltas = %#v", sink.deltas)
	}
	if text, ok := sink.deltas[0].(*llm.TextDelta); !ok || text.Text != "hello" {
		t.Fatalf("first committed delta = %#v", sink.deltas[0])
	}
}

func TestExecuteNeverFailsOverAfterFirstStreamDelta(t *testing.T) {
	var secondCalls atomic.Int32
	runtime := streamRuntime(t,
		[]configsnapshot.RouteTarget{
			routeTarget("first-target", "first", "first-model", 1),
			routeTarget("second-target", "second", "second-model", 2),
		},
		[]configsnapshot.Upstream{
			upstream("first", "test", provider.ProtocolOpenAIChatCompletions),
			upstream("second", "test", provider.ProtocolOpenAIChatCompletions),
		},
		transportFunc(func(_ context.Context, request provider.Request) (*provider.Response, error) {
			if request.URL == "https://second.example/invoke" {
				secondCalls.Add(1)
				return response(200, "data: start\n\ndata: done\n\n"), nil
			}
			return response(200, "data: start\n\ndata: bad\n\n"), nil
		}), nil,
	)
	request := llm.NewChatRequest("client-model", nil)
	request.Stream.Enabled = true
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second backend calls = %d, want 0 after stream commit", got)
	}
	if completion.Error == nil || completion.Error.Kind != llm.ErrStreamMidError {
		t.Fatalf("completion error = %+v, want stream_mid_error", completion.Error)
	}
	if len(sink.errors) != 0 {
		t.Fatalf("non-stream errors sent after commit = %d", len(sink.errors))
	}
	if len(sink.deltas) != 2 {
		t.Fatalf("sink deltas = %#v, want content plus terminal error", sink.deltas)
	}
	if terminal, ok := sink.deltas[1].(*llm.StreamErrorDelta); !ok || terminal.Error != completion.Error {
		t.Fatalf("terminal delta = %#v", sink.deltas[1])
	}
}

func TestExecuteFinalizesUnexpectedEOFAndClientCancellation(t *testing.T) {
	t.Run("unexpected EOF", func(t *testing.T) {
		var finalized atomic.Int32
		runtime := streamRuntime(t,
			[]configsnapshot.RouteTarget{routeTarget("target", "backend", "served-model", 1)},
			[]configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
			transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
				return response(200, "data: start\n\n"), nil
			}), finalizerPhase(&finalized),
		)
		request := llm.NewChatRequest("client-model", nil)
		request.Stream.Enabled = true
		sink := &recordingSink{}

		completion := runtime.Execute(context.Background(), Call{
			Request: request,
			Source:  protocol.OpenAIChatCompletionsV1,
			Sink:    sink,
		})

		if completion.Error == nil || completion.Error.Kind != llm.ErrUnexpectedEOF {
			t.Fatalf("completion error = %+v, want unexpected_eof", completion.Error)
		}
		if got := finalized.Load(); got != 1 {
			t.Fatalf("finalizer calls = %d, want 1", got)
		}
		if len(sink.deltas) != 2 {
			t.Fatalf("sink deltas = %#v", sink.deltas)
		}
		if terminal, ok := sink.deltas[1].(*llm.StreamErrorDelta); !ok || terminal.Error.Kind != llm.ErrUnexpectedEOF {
			t.Fatalf("terminal delta = %#v", sink.deltas[1])
		}
	})

	t.Run("client cancellation", func(t *testing.T) {
		var finalized atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		runtime := streamRuntime(t,
			[]configsnapshot.RouteTarget{routeTarget("target", "backend", "served-model", 1)},
			[]configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
			transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
				return response(200, "data: start\n\ndata: done\n\n"), nil
			}), finalizerPhase(&finalized),
		)
		request := llm.NewChatRequest("client-model", nil)
		request.Stream.Enabled = true
		sink := &recordingSink{onDelta: func(llm.StreamDelta) error {
			cancel()
			return nil
		}}

		completion := runtime.Execute(ctx, Call{
			Request: request,
			Source:  protocol.OpenAIChatCompletionsV1,
			Sink:    sink,
		})

		if completion.Error == nil || completion.Error.Message != "request canceled" {
			t.Fatalf("completion error = %+v, want request canceled", completion.Error)
		}
		if got := finalized.Load(); got != 1 {
			t.Fatalf("finalizer calls = %d, want 1", got)
		}
	})
}

func TestExecuteAllowsOpaqueResponseOnlyForSameEndpoint(t *testing.T) {
	for _, test := range []struct {
		name       string
		target     protocol.Endpoint
		wantOpaque bool
	}{
		{name: "same endpoint", target: protocol.OpenAIEmbeddingsV1, wantOpaque: true},
		{name: "cross endpoint", target: protocol.OpenAIChatCompletionsV1, wantOpaque: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := embeddingRuntime(t, test.target)
			sink := &recordingSink{}
			completion := runtime.Execute(context.Background(), Call{
				Request: llm.NewEmbeddingRequest("client-model", &llm.TextInput{Text: "hello"}),
				Source:  protocol.OpenAIEmbeddingsV1,
				Sink:    sink,
			})

			if test.wantOpaque {
				if completion.Error != nil || len(sink.opaque) != 1 || string(sink.opaque[0].Body) != `{"data":[1]}` {
					t.Fatalf("completion/opaque = %+v/%+v", completion, sink.opaque)
				}
				return
			}
			if len(sink.opaque) != 0 {
				t.Fatalf("cross-endpoint opaque responses = %d", len(sink.opaque))
			}
			if completion.Error == nil || len(sink.errors) != 1 {
				t.Fatalf("completion/errors = %+v/%+v", completion, sink.errors)
			}
		})
	}
}

func TestExecutePostResponseRejectionOverridesPendingOpaqueSuccess(t *testing.T) {
	postError := llm.NewError(llm.ErrContentFiltered, "response rejected by policy").WithStatus(403)
	post := testPhase{
		name: "reject.embedding.response",
		apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
			return pipeline.Outcome{Decision: pipeline.Reject, Error: postError}, nil
		},
	}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes: []configsnapshot.Route{chatRoute("route",
			routeTarget("target", "backend", "embedding-model", 1),
		)},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIEmbeddings)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress: []protocol.IngressCodec{testIngress{
			endpoint: protocol.OpenAIEmbeddingsV1,
			caps:     protocol.Capabilities{OpaquePassthrough: true},
		}},
		egress: []protocol.EgressCodec{testEmbeddingEgress{
			endpoint: protocol.OpenAIEmbeddingsV1,
			caps:     protocol.Capabilities{OpaquePassthrough: true},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, `{"data":[1]}`), nil
		}),
		post: []pipeline.Phase{post},
	})
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewEmbeddingRequest("client-model", &llm.TextInput{Text: "hello"}),
		Source:  protocol.OpenAIEmbeddingsV1,
		Sink:    sink,
	})

	if len(sink.opaque) != 0 || len(sink.errors) != 1 {
		t.Fatalf("Sink opaque/errors = %d/%d, want 0/1", len(sink.opaque), len(sink.errors))
	}
	if completion.Error != postError || completion.Response != nil {
		t.Fatalf("completion = %+v, want PostResponse error", completion)
	}
	if sink.errors[0].Kind != llm.ErrContentFiltered || sink.errors[0].Message != "response rejected by policy" {
		t.Fatalf("Sink error = %+v", sink.errors[0])
	}
}

func streamRuntime(t *testing.T, targets []configsnapshot.RouteTarget, upstreams []configsnapshot.Upstream, transport provider.Transport, observe pipeline.Phase) *Runtime {
	t.Helper()
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", targets...)},
		upstreams: upstreams,
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: protocol.OpenAIChatCompletionsV1,
			caps:     protocol.Capabilities{Streaming: true},
			state: &codecState{newStreamDecoder: func() protocol.StreamDecoder {
				return &scriptedStreamDecoder{}
			}},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transport,
		observe:   observe,
	})
}

func embeddingRuntime(t *testing.T, target protocol.Endpoint) *Runtime {
	t.Helper()
	return newRuntimeFixture(t, runtimeFixture{
		routes: []configsnapshot.Route{chatRoute("route",
			routeTarget("target", "backend", "embedding-model", 1),
		)},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", target.Protocol.String())},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress: []protocol.IngressCodec{testIngress{
			endpoint: protocol.OpenAIEmbeddingsV1,
			caps:     protocol.Capabilities{OpaquePassthrough: true},
		}},
		egress: []protocol.EgressCodec{testEmbeddingEgress{
			endpoint: target,
			caps:     protocol.Capabilities{OpaquePassthrough: true},
		}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			return response(200, `{"data":[1]}`), nil
		}),
	})
}

func finalizerPhase(count *atomic.Int32) pipeline.Phase {
	return testPhase{
		name: "observe",
		apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
			return pipeline.Outcome{Decision: pipeline.Continue}, func(context.Context, *pipeline.Exchange, pipeline.Completion) error {
				count.Add(1)
				return nil
			}
		},
	}
}
