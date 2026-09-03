package httpingress_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/bootstrap"
	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	httpingress "github.com/nyroway/nyro/go/internal/llm/ingress/http"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	providerhttp "github.com/nyroway/nyro/go/internal/llm/provider/httptransport"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func testProtocolCatalog(t *testing.T) *protocol.Catalog {
	t.Helper()
	catalog, err := bootstrap.NewLLMProtocolCatalog()
	if err != nil {
		t.Fatalf("compose protocol catalog: %v", err)
	}
	return catalog
}

func testProviderCatalog(t *testing.T) *provider.Catalog {
	t.Helper()
	catalog, err := bootstrap.NewLLMProviderCatalog()
	if err != nil {
		t.Fatalf("compose provider catalog: %v", err)
	}
	return catalog
}

type testRuntimeSource struct {
	mu        sync.Mutex
	cache     *configsnapshot.Cache
	protocols *protocol.Catalog
	providers *provider.Catalog
	transport provider.Transport
	quota     quota.Store
	observe   pipeline.Phase
	pre       []pipeline.Phase
	router    *routing.Router
	snapshot  *configsnapshot.Snapshot
	runtime   *llmruntime.Runtime
}

func (source *testRuntimeSource) Acquire() (*llmruntime.Runtime, func(), bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	snapshot := source.cache.Load()
	if snapshot == nil {
		return nil, nil, false
	}
	if source.snapshot != snapshot || source.runtime == nil {
		runtime, err := llmruntime.New(llmruntime.Config{
			Snapshot: snapshot, Protocols: source.protocols, Providers: source.providers,
			Transport: source.transport, Quota: source.quota, Observe: source.observe,
			PreDispatch: source.pre, Router: source.router,
		})
		if err != nil {
			return nil, nil, false
		}
		source.snapshot = snapshot
		source.runtime = runtime
	}
	return source.runtime, func() {}, true
}

func (source *testRuntimeSource) reload(t *testing.T, storageSource storage.Storage) {
	t.Helper()
	if err := storage.LoadAndSwap(source.cache, storageSource); err != nil {
		t.Fatalf("load cache: %v", err)
	}
}

func newTestHandler(t *testing.T, source *testRuntimeSource) http.Handler {
	t.Helper()
	handler, err := httpingress.New(source.protocols, source, httpingress.Options{})
	if err != nil {
		t.Fatalf("compose LLM HTTP ingress: %v", err)
	}
	return handler
}

func newTestSource(t *testing.T, upstreamURL string) *testRuntimeSource {
	return newTestSourceProto(t, upstreamURL, "openai-chatcompletions")
}

func newTestSourceFromStorage(t *testing.T, storageSource storage.Storage) *testRuntimeSource {
	t.Helper()
	transport, err := providerhttp.New(providerhttp.Config{
		RequestTimeout: 120 * time.Second,
		ConnectTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct Provider HTTP transport: %v", err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	source := &testRuntimeSource{
		cache:     &configsnapshot.Cache{},
		protocols: testProtocolCatalog(t),
		providers: testProviderCatalog(t),
		transport: transport,
		quota:     quota.NewMemory(),
		router:    routing.New(),
	}
	source.reload(t, storageSource)
	return source
}

func newTestSourceProto(t *testing.T, upstreamURL, protocolID string) *testRuntimeSource {
	return newTestSourceProviderProto(t, upstreamURL, "test", protocolID)
}

func newTestSourceProviderProto(t *testing.T, upstreamURL, providerID, protocolID string) *testRuntimeSource {
	t.Helper()
	core := memory.New().Storage()
	upstream, err := core.Upstreams().Create(storage.CreateUpstream{
		Name:            "test",
		Provider:        providerID,
		Protocol:        protocolID,
		BaseURL:         upstreamURL,
		CredentialsJSON: []byte(`{"api_key":"test-key"}`),
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	if _, err := core.Routes().Create(storage.CreateRoute{
		Model:     "gpt-4o",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: upstream.ID, Model: "gpt-4o"}},
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	return newTestSourceFromStorage(t, core)
}

func streamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher := writer.(http.Flusher)
		chunks := []string{
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func nonStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
}
