package httpingress_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

type streamAttemptDriver struct{}

func (streamAttemptDriver) ExtendRequest(context.Context, provider.UpstreamRuntime, llm.ModelRequest) error {
	return nil
}

func (streamAttemptDriver) Prepare(_ context.Context, upstream provider.UpstreamRuntime, wire protocol.WireRequest) (provider.Request, error) {
	return provider.Request{
		Method: wire.Method,
		URL:    provider.BuildURL(upstream.BaseURL, wire.Path),
		Body:   append([]byte(nil), wire.Body...),
		Stream: wire.Stream,
	}, nil
}

func (streamAttemptDriver) Classify(response provider.Response) provider.Classification {
	return provider.Classification{Failed: response.StatusCode >= http.StatusBadRequest}
}

func (streamAttemptDriver) ExtendResponse(context.Context, provider.UpstreamRuntime, *llm.ChatResponse) error {
	return nil
}

func (streamAttemptDriver) ExtendError(_ context.Context, _ provider.UpstreamRuntime, providerError *llm.Error) (provider.ErrorClassification, error) {
	return provider.ErrorClassification{Retryable: providerError != nil && providerError.Message == "retry this stream"}, nil
}

type streamAttemptTransport struct {
	firstCalls  atomic.Int32
	secondCalls atomic.Int32
}

func (transport *streamAttemptTransport) Do(_ context.Context, request provider.Request) (*provider.Response, error) {
	var body string
	if strings.Contains(request.URL, "first.example") {
		transport.firstCalls.Add(1)
		body = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n" +
			"data: {\"error\":{\"message\":\"retry this stream\",\"type\":\"server_error\",\"code\":\"server_error\"}}\n\n"
	} else {
		transport.secondCalls.Add(1)
		body = "data: {\"id\":\"winner-id\",\"model\":\"winner-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"winner\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"winner-id\",\"model\":\"winner-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"
	}
	return &provider.Response{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (*streamAttemptTransport) CloseIdleConnections() {}

type streamAttemptObserver struct {
	mu         sync.Mutex
	deltas     []llm.StreamDelta
	completion pipeline.Completion
	finalized  bool
}

func (*streamAttemptObserver) Name() string { return "stream-attempt-observer" }

func (observer *streamAttemptObserver) Apply(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, _ *pipeline.Exchange, completion pipeline.Completion) error {
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.completion = completion
		observer.finalized = true
		return nil
	}
}

func (observer *streamAttemptObserver) OnDelta(_ context.Context, _ *pipeline.Exchange, delta llm.StreamDelta) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.deltas = append(observer.deltas, delta)
}

func TestStreamRetryDiscardsUncommittedAttemptStateAcrossRuntimeAndHTTPSink(t *testing.T) {
	state := memory.New()
	core := state.Storage()
	first, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "first", Provider: "stream-attempt", Protocol: provider.ProtocolOpenAIChatCompletions,
		BaseURL: "https://first.example", CredentialsJSON: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create first upstream: %v", err)
	}
	second, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "second", Provider: "stream-attempt", Protocol: provider.ProtocolOpenAIChatCompletions,
		BaseURL: "https://second.example", CredentialsJSON: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create second upstream: %v", err)
	}
	if _, err := core.Routes().Create(storage.CreateRoute{
		Model: "client-model", Balance: storage.BalancePriority, EnableAuth: true,
		Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: first.ID, Model: "first-model", Priority: 1},
			{UpstreamID: second.ID, Model: "second-model", Priority: 2},
		},
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	consumer, err := core.Consumers().Create(storage.CreateConsumer{
		Name: "stream-client", Keys: []storage.CreateConsumerKey{{Name: "primary"}}, Routes: []string{"client-model"},
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	if err := core.Settings().Set("proxy.max_retries", "1"); err != nil {
		t.Fatalf("set retry limit: %v", err)
	}

	providers, err := provider.NewCatalog(provider.Registration{
		Definition: provider.Definition{ID: "stream-attempt"},
		Factory:    func() provider.Driver { return streamAttemptDriver{} },
	}, provider.Generic())
	if err != nil {
		t.Fatalf("compose Provider Catalog: %v", err)
	}
	transport := &streamAttemptTransport{}
	quotaStore := quota.NewMemory()
	observer := &streamAttemptObserver{}
	source := &testRuntimeSource{
		cache: &configsnapshot.Cache{}, protocols: testProtocolCatalog(t), providers: providers,
		transport: transport, quota: quotaStore, observe: observer,
	}
	source.reload(t, core)

	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/client-model:streamGenerateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+consumer.Keys[0].Token)
	response := httptest.NewRecorder()
	newTestHandler(t, source).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := transport.firstCalls.Load(); got != 1 {
		t.Fatalf("first attempt calls = %d, want 1", got)
	}
	if got := transport.secondCalls.Load(); got != 1 {
		t.Fatalf("second attempt calls = %d, want 1", got)
	}
	clientBody := response.Body.String()
	if !strings.Contains(clientBody, `"text":"winner"`) || !strings.Contains(clientBody, `"finishReason":"STOP"`) {
		t.Fatalf("client stream missing successful attempt: %s", clientBody)
	}
	if strings.Contains(clientBody, "usageMetadata") || strings.Contains(clientBody, "18") {
		t.Fatalf("client stream leaked failed-attempt usage or encoder state: %s", clientBody)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.finalized {
		t.Fatal("stream observer finalizer did not run")
	}
	if observer.completion.Error != nil || observer.completion.Usage != (llm.Usage{}) {
		t.Fatalf("completion = %+v, want successful zero-usage attempt", observer.completion)
	}
	if len(observer.deltas) != 3 {
		t.Fatalf("observed deltas = %#v, want MessageStart/Text/Done from successful attempt only", observer.deltas)
	}
	if _, ok := observer.deltas[0].(*llm.MessageStartDelta); !ok {
		t.Fatalf("first observed delta = %#v, want MessageStart", observer.deltas[0])
	}
	if text, ok := observer.deltas[1].(*llm.TextDelta); !ok || text.Text != "winner" {
		t.Fatalf("second observed delta = %#v, want winner Text", observer.deltas[1])
	}
	done, ok := observer.deltas[2].(*llm.DoneDelta)
	if !ok {
		t.Fatalf("third observed delta = %#v, want Done", observer.deltas[2])
	}
	if done.UsageAtDone == nil || *done.UsageAtDone != (llm.Usage{}) {
		t.Fatalf("Done usage = %#v, want successful-attempt zero usage", done.UsageAtDone)
	}
	tokens, err := quotaStore.TokenValue(context.Background(), consumer.ID, quota.MaxWindow)
	if err != nil {
		t.Fatalf("read quota tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("quota tokens = %d, want 0", tokens)
	}
}
