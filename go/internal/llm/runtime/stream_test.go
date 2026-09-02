package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
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
