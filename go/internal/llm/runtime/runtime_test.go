package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

type recordingSink struct {
	mu         sync.Mutex
	responses  []*llm.ChatResponse
	errors     []*llm.Error
	deltas     []llm.StreamDelta
	opaque     []protocol.WireResponse
	onResponse func(*llm.ChatResponse) error
	onError    func(*llm.Error) error
	onDelta    func(llm.StreamDelta) error
}

func (s *recordingSink) SendResponse(_ context.Context, response *llm.ChatResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, response)
	if s.onResponse != nil {
		return s.onResponse(response)
	}
	return nil
}

func (s *recordingSink) SendError(_ context.Context, err *llm.Error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
	if s.onError != nil {
		return s.onError(err)
	}
	return nil
}

func (s *recordingSink) SendDelta(_ context.Context, delta llm.StreamDelta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onDelta != nil {
		if err := s.onDelta(delta); err != nil {
			return err
		}
	}
	s.deltas = append(s.deltas, delta)
	return nil
}

func (s *recordingSink) SendOpaque(_ context.Context, response protocol.WireResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	response.Body = append([]byte(nil), response.Body...)
	response.Headers = cloneStringMap(response.Headers)
	s.opaque = append(s.opaque, response)
	return nil
}

var _ Sink = (*recordingSink)(nil)

type testIngress struct {
	endpoint protocol.Endpoint
	caps     protocol.Capabilities
}

func (c testIngress) Endpoint() protocol.Endpoint         { return c.endpoint }
func (c testIngress) Capabilities() protocol.Capabilities { return c.caps }
func (testIngress) EncodeError(*llm.Error) (protocol.WireResponse, error) {
	return protocol.WireResponse{}, nil
}
func (testIngress) IngressCodec() {}

type testChatEgress struct {
	endpoint protocol.Endpoint
	caps     protocol.Capabilities
	state    *codecState
}

func (c testChatEgress) Endpoint() protocol.Endpoint         { return c.endpoint }
func (c testChatEgress) Capabilities() protocol.Capabilities { return c.caps }
func (testChatEgress) EgressCodec()                          {}
func (c testChatEgress) EncodeRequest(request *llm.ChatRequest) (protocol.WireRequest, error) {
	if c.state != nil && c.state.encode != nil {
		return c.state.encode(request)
	}
	return protocol.WireRequest{Method: "POST", Path: "/invoke", Body: []byte(request.Model), Stream: request.Stream.Enabled}, nil
}
func (c testChatEgress) DecodeResponse(response protocol.WireResponse) (*llm.ChatResponse, error) {
	if c.state != nil && c.state.decode != nil {
		return c.state.decode(response)
	}
	return &llm.ChatResponse{ID: "response", Model: "served", Content: string(response.Body)}, nil
}
func (c testChatEgress) DecodeError(response protocol.WireResponse) (*llm.Error, error) {
	if c.state != nil && c.state.decodeError != nil {
		return c.state.decodeError(response)
	}
	return llm.ErrorFromStatus(uint16(response.Status), "decoded provider error").WithRaw(response.Body), nil
}
func (c testChatEgress) NewStreamDecoder() protocol.StreamDecoder {
	if c.state != nil && c.state.newStreamDecoder != nil {
		return c.state.newStreamDecoder()
	}
	return &scriptedStreamDecoder{}
}

type testEmbeddingEgress struct {
	endpoint protocol.Endpoint
	caps     protocol.Capabilities
}

func (c testEmbeddingEgress) Endpoint() protocol.Endpoint         { return c.endpoint }
func (c testEmbeddingEgress) Capabilities() protocol.Capabilities { return c.caps }
func (testEmbeddingEgress) EgressCodec()                          {}
func (testEmbeddingEgress) EncodeRequest(request *llm.EmbeddingRequest) (protocol.WireRequest, error) {
	return protocol.WireRequest{Method: "POST", Path: "/embeddings", Body: []byte(request.Model)}, nil
}
func (testEmbeddingEgress) DecodeError(response protocol.WireResponse) (*llm.Error, error) {
	return llm.ErrorFromStatus(uint16(response.Status), "decoded embedding error").WithRaw(response.Body), nil
}

type codecState struct {
	encode           func(*llm.ChatRequest) (protocol.WireRequest, error)
	decode           func(protocol.WireResponse) (*llm.ChatResponse, error)
	decodeError      func(protocol.WireResponse) (*llm.Error, error)
	newStreamDecoder func() protocol.StreamDecoder
}

type testDriver struct {
	extendRequest  func(context.Context, provider.UpstreamRuntime, llm.ModelRequest) error
	prepare        func(context.Context, provider.UpstreamRuntime, protocol.WireRequest) (provider.Request, error)
	classify       func(provider.Response) provider.Classification
	extendResponse func(context.Context, provider.UpstreamRuntime, *llm.ChatResponse) error
	extendError    func(context.Context, provider.UpstreamRuntime, *llm.Error) (provider.ErrorClassification, error)
}

func (d *testDriver) ExtendRequest(ctx context.Context, upstream provider.UpstreamRuntime, request llm.ModelRequest) error {
	if d.extendRequest != nil {
		return d.extendRequest(ctx, upstream, request)
	}
	return nil
}

func (d *testDriver) Prepare(ctx context.Context, upstream provider.UpstreamRuntime, wire protocol.WireRequest) (provider.Request, error) {
	if d.prepare != nil {
		return d.prepare(ctx, upstream, wire)
	}
	return provider.Request{
		Method: wire.Method, URL: strings.TrimRight(upstream.BaseURL, "/") + wire.Path,
		Headers: cloneStringMap(wire.Headers), Body: append([]byte(nil), wire.Body...), Stream: wire.Stream,
	}, nil
}

func (d *testDriver) Classify(response provider.Response) provider.Classification {
	if d.classify != nil {
		return d.classify(response)
	}
	return provider.Classification{
		Failed: response.StatusCode >= 400,
	}
}

func (d *testDriver) ExtendResponse(ctx context.Context, upstream provider.UpstreamRuntime, response *llm.ChatResponse) error {
	if d.extendResponse != nil {
		return d.extendResponse(ctx, upstream, response)
	}
	return nil
}

func (d *testDriver) ExtendError(ctx context.Context, upstream provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
	if d.extendError != nil {
		return d.extendError(ctx, upstream, providerError)
	}
	return provider.ErrorClassification{}, nil
}

type transportFunc func(context.Context, provider.Request) (*provider.Response, error)

func (f transportFunc) Do(ctx context.Context, request provider.Request) (*provider.Response, error) {
	return f(ctx, request)
}
func (transportFunc) CloseIdleConnections() {}

type runtimeFixture struct {
	routes    []configsnapshot.Route
	upstreams []configsnapshot.Upstream
	settings  map[string]string
	ingress   []protocol.IngressCodec
	egress    []protocol.EgressCodec
	providers []provider.Registration
	transport provider.Transport
	router    *routing.Router
	observe   pipeline.Phase
	pre       []pipeline.Phase
	post      []pipeline.Phase
	keys      []runtimeConsumerKey
}

type runtimeConsumerKey struct {
	rawKey    string
	routes    []string
	enabled   bool
	expiresAt string
}

func newRuntimeFixture(t *testing.T, fixture runtimeFixture) *Runtime {
	t.Helper()
	var builder configsnapshot.Builder
	for _, route := range fixture.routes {
		builder.SetRoute(route)
	}
	for _, upstream := range fixture.upstreams {
		builder.SetUpstream(upstream)
	}
	for key, value := range fixture.settings {
		builder.SetSetting(key, value)
	}
	for index, key := range fixture.keys {
		hash := sha256.Sum256([]byte(key.rawKey))
		preview := key.rawKey
		if len(preview) > 15 {
			preview = preview[:9] + preview[len(preview)-6:]
		}
		builder.AddConsumerKey(
			fmt.Sprintf("key-%d", index),
			fmt.Sprintf("consumer-%d", index),
			"primary",
			preview,
			fmt.Sprintf("%x", hash),
			key.enabled,
			key.expiresAt,
			key.routes,
			nil,
		)
	}
	protocols, err := protocol.NewCatalog(fixture.ingress, fixture.egress)
	if err != nil {
		t.Fatalf("protocol.NewCatalog: %v", err)
	}
	registrations := append([]provider.Registration(nil), fixture.providers...)
	registrations = append(registrations, provider.Registration{
		Definition: provider.Definition{ID: "fallback"},
		Factory:    func() provider.Driver { return &testDriver{} },
		Fallback:   true,
	})
	providers, err := provider.NewCatalog(registrations...)
	if err != nil {
		t.Fatalf("provider.NewCatalog: %v", err)
	}
	runtime, err := New(Config{
		Snapshot:     builder.Build(),
		Protocols:    protocols,
		Providers:    providers,
		Router:       fixture.router,
		Transport:    fixture.transport,
		Observe:      fixture.observe,
		PreDispatch:  fixture.pre,
		PostResponse: fixture.post,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runtime
}

func chatRoute(id string, targets ...configsnapshot.RouteTarget) configsnapshot.Route {
	return configsnapshot.Route{
		ID: id, Model: "client-model", Balance: string(routing.StrategyPriority), Enabled: true,
		Upstreams: targets,
	}
}

func routeTarget(id, upstreamID, model string, priority int32) configsnapshot.RouteTarget {
	return configsnapshot.RouteTarget{
		ID: id, RouteID: "route", UpstreamID: upstreamID, Model: model,
		Priority: priority, Weight: 1, Enabled: true,
	}
}

func upstream(id, providerID, protocolID string) configsnapshot.Upstream {
	return configsnapshot.Upstream{
		ID: id, Name: id, Provider: providerID, Protocol: protocolID,
		BaseURL: "https://" + id + ".example", Enabled: true,
	}
}

func providerRegistration(id string, driver provider.Driver) provider.Registration {
	return provider.Registration{
		Definition: provider.Definition{ID: id},
		Factory:    func() provider.Driver { return driver },
	}
}

func response(status int, body string) *provider.Response {
	return &provider.Response{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

type testPhase struct {
	name  string
	apply func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer)
}

func (p testPhase) Name() string { return p.name }
func (p testPhase) Apply(ctx context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	if p.apply != nil {
		return p.apply(ctx, exchange)
	}
	return pipeline.Outcome{Decision: pipeline.Continue}, nil
}

func TestExecuteRunsFixedProviderSequenceAfterLogicalRouteResolution(t *testing.T) {
	var sequence []string
	appendStep := func(step string) { sequence = append(sequence, step) }

	driver := &testDriver{
		extendRequest: func(_ context.Context, _ provider.UpstreamRuntime, request llm.ModelRequest) error {
			appendStep("driver request extension")
			chat := request.(*llm.ChatRequest)
			if chat.Model != "backend-model" {
				t.Fatalf("attempt model = %q, want backend-model", chat.Model)
			}
			chat.Meta.Vendor.Egress = map[string]json.RawMessage{"driver": json.RawMessage(`true`)}
			return nil
		},
		prepare: func(_ context.Context, upstream provider.UpstreamRuntime, wire protocol.WireRequest) (provider.Request, error) {
			appendStep("driver endpoint/header/signing")
			return provider.Request{Method: wire.Method, URL: upstream.BaseURL + wire.Path, Body: wire.Body}, nil
		},
		classify: func(response provider.Response) provider.Classification {
			appendStep("driver raw classification")
			return provider.Classification{Failed: response.StatusCode >= 400}
		},
		extendResponse: func(_ context.Context, _ provider.UpstreamRuntime, response *llm.ChatResponse) error {
			appendStep("driver normalized response extension")
			response.Vendor.Egress = map[string]json.RawMessage{"driver": json.RawMessage(`true`)}
			return nil
		},
	}
	codec := testChatEgress{
		endpoint: protocol.AnthropicMessagesV1,
		state: &codecState{
			encode: func(request *llm.ChatRequest) (protocol.WireRequest, error) {
				appendStep("egress encode")
				if string(request.Meta.Vendor.Egress["driver"]) != "true" {
					t.Fatal("egress codec did not observe driver request extension")
				}
				return protocol.WireRequest{Method: "POST", Path: "/invoke", Body: []byte(request.Model)}, nil
			},
			decode: func(response protocol.WireResponse) (*llm.ChatResponse, error) {
				appendStep("egress decode")
				return &llm.ChatResponse{ID: "ok", Model: "backend-model", Content: string(response.Body)}, nil
			},
		},
	}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "backend-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolAnthropicMessages)},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{codec},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			appendStep("transport")
			return response(200, "provider response"), nil
		}),
		pre: []pipeline.Phase{testPhase{
			name: "assert.logical.route",
			apply: func(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
				if exchange.Route.ID != "route" || exchange.Route.Model != "client-model" {
					t.Fatalf("logical route = %+v", exchange.Route)
				}
				if exchange.Target.UpstreamID != "" {
					t.Fatalf("backend selected before Dispatch: %+v", exchange.Target)
				}
				return pipeline.Outcome{Decision: pipeline.Continue}, nil
			},
		}},
	})

	request := llm.NewChatRequest("client-model", nil)
	sink := &recordingSink{}
	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	wantSequence := []string{
		"driver request extension",
		"egress encode",
		"driver endpoint/header/signing",
		"transport",
		"driver raw classification",
		"egress decode",
		"driver normalized response extension",
	}
	if !slices.Equal(sequence, wantSequence) {
		t.Fatalf("provider sequence = %q, want %q", sequence, wantSequence)
	}
	if completion.Error != nil || completion.Response == nil || completion.Response.Content != "provider response" {
		t.Fatalf("completion = %+v", completion)
	}
	if len(sink.responses) != 1 || len(sink.errors) != 0 || len(sink.opaque) != 0 {
		t.Fatalf("sink responses/errors/opaque = %d/%d/%d", len(sink.responses), len(sink.errors), len(sink.opaque))
	}
	if request.Model != "client-model" || !request.Meta.Vendor.IsEmpty() {
		t.Fatalf("base request was mutated: %+v", request)
	}
}

func TestExecuteRunsErrorDecodeAndDriverExtensionInProviderSequence(t *testing.T) {
	var sequence []string
	appendStep := func(step string) { sequence = append(sequence, step) }

	driver := &testDriver{
		extendRequest: func(context.Context, provider.UpstreamRuntime, llm.ModelRequest) error {
			appendStep("driver request extension")
			return nil
		},
		prepare: func(_ context.Context, upstream provider.UpstreamRuntime, wire protocol.WireRequest) (provider.Request, error) {
			appendStep("driver endpoint/header/signing")
			return provider.Request{Method: wire.Method, URL: upstream.BaseURL + wire.Path, Body: wire.Body}, nil
		},
		classify: func(provider.Response) provider.Classification {
			appendStep("driver raw classification")
			return provider.Classification{Failed: true}
		},
		extendError: func(_ context.Context, _ provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
			appendStep("driver normalized error extension")
			if providerError.Kind != llm.ErrContentFiltered || providerError.Message != "decoded vendor refusal" {
				t.Fatalf("error before Driver extension = %+v", providerError)
			}
			providerError.Message = "provider: " + providerError.Message
			return provider.ErrorClassification{}, nil
		},
	}
	codec := testChatEgress{
		endpoint: protocol.AnthropicMessagesV1,
		state: &codecState{
			encode: func(request *llm.ChatRequest) (protocol.WireRequest, error) {
				appendStep("egress encode")
				return protocol.WireRequest{Method: "POST", Path: "/invoke", Body: []byte(request.Model)}, nil
			},
			decodeError: func(response protocol.WireResponse) (*llm.Error, error) {
				appendStep("egress error decode")
				if response.Status != 422 || string(response.Body) != `{"type":"content_filter","message":"refused"}` {
					t.Fatalf("wire error = %+v", response)
				}
				return llm.NewError(llm.ErrContentFiltered, "decoded vendor refusal").WithStatus(422).WithRaw(response.Body), nil
			},
		},
	}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "backend-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolAnthropicMessages)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{codec},
		providers: []provider.Registration{providerRegistration("test", driver)},
		transport: transportFunc(func(_ context.Context, _ provider.Request) (*provider.Response, error) {
			appendStep("transport")
			return response(422, `{"type":"content_filter","message":"refused"}`), nil
		}),
	})
	sink := &recordingSink{}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	wantSequence := []string{
		"driver request extension",
		"egress encode",
		"driver endpoint/header/signing",
		"transport",
		"driver raw classification",
		"egress error decode",
		"driver normalized error extension",
	}
	if !slices.Equal(sequence, wantSequence) {
		t.Fatalf("provider error sequence = %q, want %q", sequence, wantSequence)
	}
	if completion.Error == nil || completion.Error.Kind != llm.ErrContentFiltered || completion.Error.Message != "provider: decoded vendor refusal" {
		t.Fatalf("completion error = %+v", completion.Error)
	}
	if len(sink.errors) != 1 || sink.errors[0].Message != "provider: decoded vendor refusal" {
		t.Fatalf("sink errors = %+v", sink.errors)
	}
}

func TestNewRejectsMissingRuntimeDependencies(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("New(Config{}) succeeded")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("New error = %v", err)
	}
}

func TestIngressQueriesUseRuntimeBoundSnapshot(t *testing.T) {
	runtime := newRuntimeFixture(t, runtimeFixture{
		settings: map[string]string{"proxy.max_body_bytes": "1234"},
		routes: []configsnapshot.Route{
			{ID: "open", Model: "open-model", Enabled: true},
			{ID: "secret", Model: "secret-model", Enabled: true, EnableAuth: true},
		},
		ingress: []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:  []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		keys: []runtimeConsumerKey{{
			rawKey:  "granted-token",
			routes:  []string{"secret-model"},
			enabled: true,
		}},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "unused"), nil
		}),
	})

	if got := runtime.MaxBodyBytes(); got != 1234 {
		t.Fatalf("MaxBodyBytes = %d, want 1234", got)
	}
	if got := runtime.ClientModelNames(authn.Credentials{}); !slices.Equal(got, []string{"open-model"}) {
		t.Fatalf("anonymous models = %q, want [open-model]", got)
	}
	if got := runtime.ClientModelNames(authn.Credentials{APIKey: "granted-token"}); !slices.Equal(got, []string{"open-model", "secret-model"}) {
		t.Fatalf("granted models = %q, want [open-model secret-model]", got)
	}
}

func TestExecuteDeliversTerminalResponseBeforeFinalizers(t *testing.T) {
	var events []string
	var finalized pipeline.Completion
	observe := testPhase{name: "observe", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, _ *pipeline.Exchange, completion pipeline.Completion) error {
			events = append(events, "finalize")
			finalized = completion
			return nil
		}
	}}
	runtime := terminalRuntime(t, observe, nil)
	sink := &recordingSink{onResponse: func(*llm.ChatResponse) error {
		events = append(events, "sink response")
		return nil
	}}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if !slices.Equal(events, []string{"sink response", "finalize"}) {
		t.Fatalf("terminal order = %q", events)
	}
	if finalized.Response == nil || finalized.Error != nil || completion.Response != finalized.Response || completion.Error != nil {
		t.Fatalf("finalized/returned completion = %+v / %+v", finalized, completion)
	}
}

func TestExecuteNormalizesRunErrorAndDeliversBeforeFinalizers(t *testing.T) {
	var events []string
	var finalized pipeline.Completion
	var finalizedStatus int
	observe := testPhase{name: "observe", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, exchange *pipeline.Exchange, completion pipeline.Completion) error {
			events = append(events, "finalize")
			finalized = completion
			finalizedStatus = exchange.Status
			return nil
		}
	}}
	broken := testPhase{name: "broken.extension", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Decision(255)}, nil
	}}
	runtime := terminalRuntime(t, observe, []pipeline.Phase{broken})
	sink := &recordingSink{onError: func(*llm.Error) error {
		events = append(events, "sink error")
		return nil
	}}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if !slices.Equal(events, []string{"sink error", "finalize"}) {
		t.Fatalf("terminal order = %q", events)
	}
	if finalized.Error == nil || !strings.Contains(finalized.Error.Message, "invalid decision 255") || finalized.Response != nil || finalizedStatus != statusBadGateway {
		t.Fatalf("finalizer saw completion/status = %+v / %d", finalized, finalizedStatus)
	}
	if completion.Error != finalized.Error || completion.Response != nil {
		t.Fatalf("returned completion = %+v", completion)
	}
}

func TestExecuteSinkResponseFailureBecomesUnambiguousFinalCompletion(t *testing.T) {
	var finalized pipeline.Completion
	var finalizedStatus int
	observe := testPhase{name: "observe", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, exchange *pipeline.Exchange, completion pipeline.Completion) error {
			finalized = completion
			finalizedStatus = exchange.Status
			return nil
		}
	}}
	runtime := terminalRuntime(t, observe, nil)
	sink := &recordingSink{onResponse: func(*llm.ChatResponse) error {
		return errors.New("client write failed")
	}}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("client-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if finalized.Error == nil || !strings.Contains(finalized.Error.Message, "client write failed") || finalized.Response != nil || finalizedStatus != statusBadGateway {
		t.Fatalf("finalizer saw completion/status = %+v / %d", finalized, finalizedStatus)
	}
	if completion.Error != finalized.Error || completion.Response != nil {
		t.Fatalf("returned completion = %+v", completion)
	}
	if len(sink.responses) != 1 || len(sink.errors) != 0 {
		t.Fatalf("Sink response/error attempts = %d/%d, want 1/0", len(sink.responses), len(sink.errors))
	}
}

func TestExecutePropagatesSendErrorFailureBeforeFinalizers(t *testing.T) {
	var finalized pipeline.Completion
	var finalizedStatus int
	observe := testPhase{name: "observe", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, exchange *pipeline.Exchange, completion pipeline.Completion) error {
			finalized = completion
			finalizedStatus = exchange.Status
			return nil
		}
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		ingress: []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:  []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "unused"), nil
		}),
		observe: observe,
	})
	sink := &recordingSink{onError: func(*llm.Error) error {
		return errors.New("client error write failed")
	}}

	completion := runtime.Execute(context.Background(), Call{
		Request: llm.NewChatRequest("missing-model", nil),
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    sink,
	})

	if finalized.Error == nil || !strings.Contains(finalized.Error.Message, "client error write failed") || finalized.Response != nil || finalizedStatus != statusBadGateway {
		t.Fatalf("finalizer saw completion/status = %+v / %d", finalized, finalizedStatus)
	}
	if completion.Error != finalized.Error || completion.Response != nil {
		t.Fatalf("returned completion = %+v", completion)
	}
	if len(sink.errors) != 1 {
		t.Fatalf("Sink error attempts = %d, want 1", len(sink.errors))
	}
}

func TestExecutePropagatesImmediateSendErrorFailure(t *testing.T) {
	runtime := terminalRuntime(t, nil, nil)
	sink := &recordingSink{onError: func(*llm.Error) error {
		return errors.New("immediate error write failed")
	}}

	completion := runtime.Execute(context.Background(), Call{Sink: sink})

	if completion.Error == nil || !strings.Contains(completion.Error.Message, "immediate error write failed") || completion.Response != nil {
		t.Fatalf("completion = %+v", completion)
	}
	if len(sink.errors) != 1 {
		t.Fatalf("Sink error attempts = %d, want 1", len(sink.errors))
	}
}

func TestExecuteClonesCallerRequestBeforePreDispatch(t *testing.T) {
	request := llm.NewChatRequest("client-model", nil)
	request.Meta.Vendor.Ingress = map[string]json.RawMessage{"seed": json.RawMessage(`"original"`)}
	pre := testPhase{name: "mutate.request", apply: func(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		attempt := exchange.Request.(*llm.ChatRequest)
		attempt.Meta.Vendor.Ingress["seed"][0] = 'X'
		attempt.Meta.Vendor.Ingress["added"] = json.RawMessage(`true`)
		return pipeline.Outcome{Decision: pipeline.Continue}, nil
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "ok"), nil
		}),
		pre: []pipeline.Phase{pre},
	})

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error != nil {
		t.Fatalf("completion error = %v", completion.Error)
	}
	if got := string(request.Meta.Vendor.Ingress["seed"]); got != `"original"` {
		t.Fatalf("caller extension = %s, want original", got)
	}
	if _, found := request.Meta.Vendor.Ingress["added"]; found {
		t.Fatal("PreDispatch mutation leaked into caller request")
	}
}

func TestExecuteKeepsAuthorizedRouteAndModelAfterPreDispatchMutation(t *testing.T) {
	var providerURL string
	var providerModel string
	var finalizedRoute pipeline.LogicalRoute
	originalRoute := configsnapshot.Route{
		ID: "authorized-route", Model: "client-model", Balance: string(routing.StrategyPriority), Enabled: true,
		Upstreams: []configsnapshot.RouteTarget{routeTarget("authorized-target", "authorized-backend", "*", 1)},
	}
	otherRoute := configsnapshot.Route{
		ID: "substituted-route", Model: "other-model", Balance: string(routing.StrategyPriority), Enabled: true,
		Upstreams: []configsnapshot.RouteTarget{routeTarget("other-target", "other-backend", "other-provider-model", 1)},
	}
	pre := testPhase{name: "mutate.route", apply: func(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		if !exchange.Authorization.Allowed {
			t.Fatal("PreDispatch ran before authorization")
		}
		exchange.Route = pipeline.LogicalRoute{ID: otherRoute.ID, Model: otherRoute.Model}
		exchange.Request.SetModelID(otherRoute.Model)
		return pipeline.Outcome{Decision: pipeline.Continue}, nil
	}}
	observe := testPhase{name: "observe", apply: func(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
		return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, exchange *pipeline.Exchange, _ pipeline.Completion) error {
			finalizedRoute = exchange.Route
			return nil
		}
	}}
	runtime := newRuntimeFixture(t, runtimeFixture{
		routes: []configsnapshot.Route{originalRoute, otherRoute},
		upstreams: []configsnapshot.Upstream{
			upstream("authorized-backend", "test", provider.ProtocolOpenAIChatCompletions),
			upstream("other-backend", "test", provider.ProtocolOpenAIChatCompletions),
		},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(_ context.Context, request provider.Request) (*provider.Response, error) {
			providerURL = request.URL
			providerModel = string(request.Body)
			return response(200, "ok"), nil
		}),
		observe: observe,
		pre:     []pipeline.Phase{pre},
	})
	request := llm.NewChatRequest("client-model", nil)

	completion := runtime.Execute(context.Background(), Call{
		Request: request,
		Source:  protocol.OpenAIChatCompletionsV1,
		Sink:    &recordingSink{},
	})

	if completion.Error != nil {
		t.Fatalf("completion error = %v", completion.Error)
	}
	if !strings.Contains(providerURL, "authorized-backend.example") || providerModel != "client-model" {
		t.Fatalf("Provider target URL/model = %q/%q, want authorized backend/client-model", providerURL, providerModel)
	}
	if finalizedRoute.ID != originalRoute.ID || finalizedRoute.Model != originalRoute.Model {
		t.Fatalf("finalized logical route = %+v, want authorized route", finalizedRoute)
	}
	if request.Model != "client-model" {
		t.Fatalf("caller model = %q, want client-model", request.Model)
	}
}

func terminalRuntime(t *testing.T, observe pipeline.Phase, post []pipeline.Phase) *Runtime {
	t.Helper()
	return newRuntimeFixture(t, runtimeFixture{
		routes:    []configsnapshot.Route{chatRoute("route", routeTarget("target", "backend", "served-model", 1))},
		upstreams: []configsnapshot.Upstream{upstream("backend", "test", provider.ProtocolOpenAIChatCompletions)},
		settings:  map[string]string{"proxy.max_retries": "1"},
		ingress:   []protocol.IngressCodec{testIngress{endpoint: protocol.OpenAIChatCompletionsV1}},
		egress:    []protocol.EgressCodec{testChatEgress{endpoint: protocol.OpenAIChatCompletionsV1}},
		providers: []provider.Registration{providerRegistration("test", &testDriver{})},
		transport: transportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return response(200, "terminal response"), nil
		}),
		observe: observe,
		post:    post,
	})
}
