package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
)

func TestExecuteRetriesConfiguredStatusesUsingCurrentAttemptSemantics(t *testing.T) {
	var attempts atomic.Int32
	runtime := retryRuntime(t, []configsnapshot.RouteTarget{
		routeTarget("target", "backend", "served-model", 1),
	}, []configsnapshot.Upstream{
		upstream("backend", "test", provider.ProtocolOpenAIChatCompletions),
	}, map[string]string{
		"proxy.max_retries":     "3",
		"proxy.retry_on_status": `[503]`,
	}, transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
		if attempts.Add(1) < 3 {
			return response(503, `{"error":{"message":"retry"}}`), nil
		}
		return response(200, `{"ok":true}`), nil
	}))

	sink := &recordingSink{}
	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := attempts.Load(); got != 3 {
		t.Fatalf("transport attempts = %d, want 3 total attempts", got)
	}
	if completion.Error != nil || completion.Response == nil {
		t.Fatalf("completion = %+v", completion)
	}
	if len(sink.responses) != 1 || len(sink.errors) != 0 {
		t.Fatalf("sink responses/errors = %d/%d", len(sink.responses), len(sink.errors))
	}
}

func TestExecuteFailsOverAfterBackendRetryBudget(t *testing.T) {
	var firstAttempts atomic.Int32
	var secondAttempts atomic.Int32
	runtime := retryRuntime(t, []configsnapshot.RouteTarget{
		routeTarget("first-target", "first", "first-model", 1),
		routeTarget("second-target", "second", "second-model", 2),
	}, []configsnapshot.Upstream{
		upstream("first", "test", provider.ProtocolOpenAIChatCompletions),
		upstream("second", "test", provider.ProtocolOpenAIChatCompletions),
	}, map[string]string{
		"proxy.max_retries":     "2",
		"proxy.retry_on_status": `[503]`,
	}, transportFunc(func(_ context.Context, request provider.Request) (*provider.Response, error) {
		switch {
		case strings.Contains(request.URL, "first.example"):
			firstAttempts.Add(1)
			return response(503, `{"error":{"message":"first unavailable"}}`), nil
		case strings.Contains(request.URL, "second.example"):
			secondAttempts.Add(1)
			return response(200, "second response"), nil
		default:
			t.Fatalf("unexpected request URL %q", request.URL)
			return nil, nil
		}
	}))

	sink := &recordingSink{}
	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := firstAttempts.Load(); got != 2 {
		t.Fatalf("first backend attempts = %d, want 2", got)
	}
	if got := secondAttempts.Load(); got != 1 {
		t.Fatalf("second backend attempts = %d, want 1", got)
	}
	if completion.Error != nil || completion.Response == nil || completion.Response.Content != "second response" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestExecuteRetriesSemanticClassificationOnCustomStatus(t *testing.T) {
	var attempts atomic.Int32
	driver := &testDriver{classify: func(response provider.Response) provider.Classification {
		if response.StatusCode == 409 {
			return provider.Classification{Retryable: true}
		}
		return provider.Classification{}
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings: map[string]string{
			"proxy.max_retries":     "2",
			"proxy.retry_on_status": `[503]`,
		},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			if attempts.Add(1) == 1 {
				return response(409, `{"error":{"message":"provider asks retry"}}`), nil
			}
			return response(200, "ok"), nil
		}),
	})
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if got := attempts.Load(); got != 2 {
		t.Fatalf("transport attempts = %d, want 2 for semantic retryability", got)
	}
	if completion.Error != nil || completion.Response == nil || completion.Response.Content != "ok" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestExecuteUsesPostDecodeProviderClassificationForSameStatusBodies(t *testing.T) {
	const (
		transientBody = `{"error":{"message":"capacity warming","code":"warming"}}`
		permanentBody = `{"error":{"message":"account blocked","code":"blocked"}}`
	)
	for _, test := range []struct {
		name         string
		body         string
		wantAttempts int32
		wantSuccess  bool
	}{
		{name: "body classified transient", body: transientBody, wantAttempts: 2, wantSuccess: true},
		{name: "body classified permanent", body: permanentBody, wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			driver := &testDriver{extendError: func(_ context.Context, _ provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
				switch string(providerError.Raw) {
				case transientBody:
					return provider.ErrorClassification{Retryable: true}, nil
				case permanentBody:
					return provider.ErrorClassification{}, nil
				default:
					t.Fatalf("Driver received unexpected normalized Raw body %q", providerError.Raw)
					return provider.ErrorClassification{}, nil
				}
			}}
			runtime := newRuntimeFixture(t, runtimeFixture{
				routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
				upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
				settings: map[string]string{
					"proxy.max_retries":     "2",
					"proxy.retry_on_status": `[503]`,
				},
				ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
				egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
				providers: []provider.Registration{providerRegistration("test", driver)},
				transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
					attempt := attempts.Add(1)
					if test.wantSuccess && attempt == 2 {
						return response(200, "ok"), nil
					}
					return response(409, test.body), nil
				}),
			})
			sink := &recordingSink{}

			completion := runtime.Execute(context.Background(), Call{
				Request: llm.NewChatRequest("client-model", nil),
				Source:  protocol.OpenAIChatCompletionsV1,
				Sink:    sink,
			})

			if got := attempts.Load(); got != test.wantAttempts {
				t.Fatalf("transport attempts = %d, want %d for status 409 body %q", got, test.wantAttempts, test.body)
			}
			if test.wantSuccess {
				if completion.Error != nil || completion.Response == nil || completion.Response.Content != "ok" {
					t.Fatalf("completion = %+v", completion)
				}
				return
			}
			if completion.Error == nil || completion.Response != nil {
				t.Fatalf("completion = %+v, want permanent Provider error", completion)
			}
		})
	}
}

func TestExecuteOrdinaryNonRetriedClientErrorRestoresProviderHealth(t *testing.T) {
	router := routing.New()
	target := routeTarget("target", "backend", "served-model", 1)
	key := routing.KeyOf(routing.Target{UpstreamID: target.UpstreamID, Model: target.Model})
	router.Record(key, false, 0)
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", target)},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings: map[string]string{
			"proxy.max_retries":     "2",
			"proxy.retry_on_status": `[503]`,
		},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(418, `{"error":{"message":"ordinary client error"}}`), nil
		}),
		router: router,
	})

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error == nil || completion.Response != nil {
		t.Fatalf("completion = %+v, want non-retried Provider error", completion)
	}
	if !router.IsHealthy(key) {
		t.Fatal("ordinary non-retried 4xx response did not restore Provider health")
	}
}

func TestExecuteMarksExplicitProviderHealthFailureUnhealthy(t *testing.T) {
	router := routing.New()
	target := routeTarget("target", "backend", "served-model", 1)
	key := routing.KeyOf(routing.Target{UpstreamID: target.UpstreamID, Model: target.Model})
	driver := &testDriver{extendError: func(context.Context, provider.UpstreamRuntime, *llm.Error) (provider.ErrorClassification, error) {
		return provider.ErrorClassification{Unhealthy: true}, nil
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", target)},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(418, `{"error":{"message":"classified failure"}}`), nil
		}),
		router: router,
	})

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error == nil {
		t.Fatal("completion error = nil")
	}
	if router.IsHealthy(key) {
		t.Fatal("explicit Provider health failure was recorded healthy")
	}
}

func TestExecuteDoesNotLeakProviderExtensionsAcrossAttempts(t *testing.T) {
	var attempts atomic.Int32
	var leaked atomic.Bool
	driver := &testDriver{
		extendRequest: func(_ context.Context, _ provider.UpstreamRuntime, request llm.ModelRequest) error {
			chat := request.(*llm.ChatRequest)
			if _, found := chat.Meta.Vendor.Egress["provider_attempt"]; found {
				leaked.Store(true)
			}
			if got := string(chat.Meta.Vendor.Egress["seed"]); got != `"original"` {
				leaked.Store(true)
			}
			chat.Meta.Vendor.Egress["seed"][0] = 'X'
			chat.Meta.Vendor.Egress["provider_attempt"] = json.RawMessage(`true`)
			return nil
		},
	}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "provider-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings: map[string]string{
			"proxy.max_retries":     "2",
			"proxy.retry_on_status": `[503]`,
		},
		ingress: []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:  []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{
			providerRegistration("test", driver),
		},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			if attempts.Add(1) == 1 {
				return response(503, "retry"), nil
			}
			return response(200, "ok"), nil
		}),
	})
	request := llm.NewChatRequest("client-model", nil)
	request.Meta.Vendor.Egress = map[string]json.RawMessage{"seed": json.RawMessage(`"original"`)}

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error != nil {
		t.Fatalf("completion error = %v", completion.Error)
	}
	if leaked.Load() {
		t.Fatal("a provider extension from one attempt leaked into another")
	}
	if request.Model != "client-model" {
		t.Fatalf("base request model = %q, want client-model", request.Model)
	}
	if got := string(request.Meta.Vendor.Egress["seed"]); got != `"original"` {
		t.Fatalf("base request extension = %s, want original", got)
	}
	if _, found := request.Meta.Vendor.Egress["provider_attempt"]; found {
		t.Fatal("provider extension leaked into the base request")
	}
}

func TestExecutePreservesSameProtocolProviderError(t *testing.T) {
	body := `{"error":{"message":"provider detail"}}`
	runtime := errorRuntime(t, protocol.OpenAIChatCompletionsV1, protocol.OpenAIChatCompletionsV1, body)
	sink := &recordingSink{}
	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if completion.Error == nil || completion.Error.StatusCode == nil || *completion.Error.StatusCode != 418 {
		t.Fatalf("completion error = %+v", completion.Error)
	}
	if len(sink.opaque) != 1 {
		t.Fatalf("opaque responses = %d, want 1", len(sink.opaque))
	}
	if got := sink.opaque[0]; got.Status != 418 || string(got.Body) != body || got.Headers["Content-Type"] != "application/problem+json" || len(got.Headers) != 1 {
		t.Fatalf("opaque response = %+v", got)
	}
	if len(sink.errors) != 0 {
		t.Fatalf("normalized errors = %d, want 0", len(sink.errors))
	}
}

func TestExecuteNormalizesCrossProtocolProviderError(t *testing.T) {
	body := `{"error":{"message":"provider detail"}}`
	runtime := errorRuntime(t, protocol.OpenAIChatCompletionsV1, protocol.AnthropicMessagesV1, body)
	sink := &recordingSink{}
	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if len(sink.opaque) != 0 {
		t.Fatalf("opaque responses = %d, want 0 across endpoints", len(sink.opaque))
	}
	if len(sink.errors) != 1 {
		t.Fatalf("normalized errors = %d, want 1", len(sink.errors))
	}
	got := sink.errors[0]
	if completion.Error == got {
		t.Fatal("cross-Endpoint Sink received the diagnostic Completion error object directly")
	}
	if got.StatusCode == nil || *got.StatusCode != 418 || len(got.Raw) != 0 {
		t.Fatalf("normalized error = %+v; completion = %+v", got, completion)
	}
	if string(completion.Error.Raw) != body {
		t.Fatalf("diagnostic completion raw = %q, want Provider body", completion.Error.Raw)
	}
}

type closeTrackingBody struct {
	closed atomic.Bool
}

func (*closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (body *closeTrackingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestExecuteClosesResponseBodyWhenTransportReturnsResponseAndError(t *testing.T) {
	body := &closeTrackingBody{}
	runtime := retryRuntime(t,
		[]configsnapshot.RouteTarget{routeTarget("target", "backend", "served-model", 1)},
		[]configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		map[string]string{"proxy.max_retries": "1"},
		transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return &provider.Response{StatusCode: 502, Body: body}, errors.New("transport failed after receiving headers")
		}),
	)

	runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if !body.closed.Load() {
		t.Fatal("response body was not closed when Transport returned response and error")
	}
}

func TestExecuteRejectsNilProviderResponseBody(t *testing.T) {
	runtime := retryRuntime(t,
		[]configsnapshot.RouteTarget{routeTarget("target", "backend", "served-model", 1)},
		[]configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		map[string]string{"proxy.max_retries": "1"},
		transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return &provider.Response{StatusCode: 200}, nil
		}),
	)

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error == nil {
		t.Fatalf("completion error = %+v", completion.Error)
	}
}

func retryRuntime(t *testing.T, targets []configsnapshot.RouteTarget, upstreams []configsnapshot.Upstream, settings map[string]string, transport provider.Transport) *Runtime {
	t.Helper()
	driver := &testDriver{}
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", targets...)},
		upstreams: upstreams,
		settings:  settings,
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transport,
	})
}

func errorRuntime(t *testing.T, source, target protocol.Endpoint, body string) *Runtime {
	t.Helper()
	driver := &testDriver{}
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", target.Protocol.String())},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress: []protocol.IngressCodec{testIngress{
			endpoint: source,
			caps:     protocol.Capabilities{ErrorPassthrough: true},
		}},
		egress: []protocol.EgressCodec{testChatEgress{
			endpoint: target,
			caps:     protocol.Capabilities{ErrorPassthrough: true},
		}},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			providerResponse := response(418, body)
			providerResponse.Headers["Content-Type"] = []string{"application/problem+json"}
			providerResponse.Headers["Set-Cookie"] = []string{"session=provider-secret"}
			providerResponse.Headers["X-Provider-Secret"] = []string{"do-not-forward"}
			return providerResponse, nil
		}),
	})
}
