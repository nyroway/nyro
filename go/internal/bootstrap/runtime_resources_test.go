package bootstrap

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
	platformstate "github.com/nyroway/nyro/go/internal/platform/state"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/telemetry"
	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

func TestStateBindingsReuseUnchangedBackendWithoutResettingQuota(t *testing.T) {
	pool := newStatePool()
	first := newStateBinding(pool, platformstate.Config{Kind: platformstate.KindMemory})
	second := newStateBinding(pool, platformstate.Config{Kind: platformstate.KindMemory})
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	limits := []quota.RequestLimit{{Limit: 1, Window: time.Minute}}
	if allowed, err := first.Store().AdmitRequest(context.Background(), "consumer", limits); err != nil || !allowed {
		t.Fatalf("first admission = %v, %v", allowed, err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if allowed, err := second.Store().AdmitRequest(context.Background(), "consumer", limits); err != nil || allowed {
		t.Fatalf("second generation admission = %v, %v; unchanged State must preserve counters", allowed, err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !second.Ready() {
		t.Fatal("closing old generation made shared State unavailable to active generation")
	}
}

func TestTelemetryBindingsReuseUnchangedPrometheusListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	cfg := telemetry.Config{Metrics: telemetry.SignalConfig{
		Kind:   schema.ExporterKindPrometheus,
		Params: map[string]string{"listen": addr, "path": "/metrics"},
	}}
	pool := newTelemetryPool()
	first := newTelemetryBinding(pool, cfg)
	second := newTelemetryBinding(pool, cfg)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("second unchanged telemetry Start collided with listener: %v", err)
	}
	defer second.Close(context.Background())
	waitHTTP(t, "http://"+addr+"/metrics", true)
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHTTP(t, "http://"+addr+"/metrics", true)
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHTTP(t, "http://"+addr+"/metrics", false)
}

func TestProviderTransportActivatesAndClosesWithGeneration(t *testing.T) {
	roundTripper := &lifecycleRoundTripper{}
	transport := newProviderTransport(llmruntime.SettingsFromSnapshot(nil), roundTripper)
	request := provider.Request{Method: http.MethodPost, URL: "http://provider.example/v1", Body: []byte("request")}
	if response, err := transport.Do(context.Background(), request); err == nil || response != nil {
		t.Fatalf("Do before Start = %#v, %v", response, err)
	}
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := transport.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := transport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := roundTripper.closes.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", got)
	}
	if response, err := transport.Do(context.Background(), request); err == nil || response != nil {
		t.Fatalf("Do after Close = %#v, %v", response, err)
	}
}

func TestProviderTransportRetiresOnlyAfterGenerationLeaseDrains(t *testing.T) {
	roundTripper := &lifecycleRoundTripper{}
	transport := newProviderTransport(llmruntime.SettingsFromSnapshot(nil), roundTripper)
	host := kernel.NewHost[*ApplicationRuntime]()
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Version: "v1", Fingerprint: "fp1",
		Value:      &ApplicationRuntime{Snapshot: (&configsnapshot.Builder{}).Build(), LLM: new(llmruntime.Runtime)},
		Components: []kernel.Component{{ID: "provider-transport", Lifecycle: transport}},
	}); err != nil {
		t.Fatal(err)
	}
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Host.Acquire() rejected active generation")
	}
	response, err := transport.Do(context.Background(), provider.Request{Method: http.MethodPost, URL: "http://provider.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Version: "v2", Fingerprint: "fp2",
		Value:      &ApplicationRuntime{Snapshot: (&configsnapshot.Builder{}).Build(), LLM: new(llmruntime.Runtime)},
		Components: []kernel.Component{{ID: "provider-transport", Lifecycle: newProviderTransport(llmruntime.SettingsFromSnapshot(nil), nil)}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := roundTripper.closes.Load(); got != 0 {
		t.Fatalf("transport closed %d times while old generation lease was active", got)
	}
	lease.Release()
	deadline := time.Now().Add(time.Second)
	for roundTripper.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := roundTripper.closes.Load(); got != 1 {
		t.Fatalf("transport closes after lease release = %d, want 1", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultLLMFactoryBuildsInactiveResourcesForKernelActivation(t *testing.T) {
	protocols, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewLLMProviderCatalog()
	if err != nil {
		t.Fatal(err)
	}
	stateResources := newStatePool()
	telemetryResources := newTelemetryPool()
	factory := newDefaultLLMFactory(protocols, providers, stateResources, telemetryResources, nil)
	graph := &GraphBuilder{Protocols: protocols, Providers: providers, LLMFactory: factory}
	candidate, err := graph.Build(context.Background(), (&configsnapshot.Builder{}).Build(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Value == nil || candidate.Value.LLM == nil {
		t.Fatal("GraphBuilder returned no Application LLM Runtime")
	}
	if candidate.Value.isReady() {
		t.Fatal("candidate resources became ready before Kernel Start")
	}
	host := kernel.NewHost[*ApplicationRuntime]()
	if _, err := host.Activate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if !NewRuntimeSource(host).Ready() {
		t.Fatal("activated Application generation is not ready")
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stateResources.Close(); err != nil {
		t.Fatal(err)
	}
	if err := telemetryResources.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type lifecycleRoundTripper struct{ closes atomic.Int32 }

func (*lifecycleRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}, nil
}

func (roundTripper *lifecycleRoundTripper) CloseIdleConnections() { roundTripper.closes.Add(1) }

func waitHTTP(t *testing.T, url string, wantUp bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if response != nil {
			_ = response.Body.Close()
		}
		if (err == nil) == wantUp {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("endpoint %s up=%v, want %v", url, !wantUp, wantUp)
}
