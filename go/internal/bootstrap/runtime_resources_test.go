package bootstrap

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	providerhttp "github.com/nyroway/nyro/go/internal/llm/provider/httptransport"
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

func TestStateBindingRejectsUnhealthyReusedRedisAndMarksExistingBindingsUnready(t *testing.T) {
	config := platformstate.Config{Kind: platformstate.KindRedis, URL: "redis://alice:secret@redis.example:6379/0"}
	var healthy atomic.Bool
	healthy.Store(true)
	resource := &stateResource{
		config: config,
		store:  quota.NewMemory(),
		ping: func(context.Context) error {
			if healthy.Load() {
				return nil
			}
			return errors.New("dial redis with password secret failed")
		},
		probe: func(context.Context) error { return nil },
	}
	resource.healthy.Store(true)
	pool := newStatePool()
	pool.entries[config] = resource
	first := newStateBinding(pool, config)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	healthy.Store(false)
	second := newStateBinding(pool, config)
	err := second.Start(context.Background())
	if err == nil {
		_ = second.Close(context.Background())
		t.Fatal("same-config candidate activated without rechecking shared Redis")
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("State activation error leaked Redis credentials: %q", err)
	}
	if first.Ready() {
		t.Fatal("existing binding did not observe shared Redis health failure")
	}
}

func TestStateLeaseReleaseFailureMarksEverySharedBindingUnreadyAndIsIdempotent(t *testing.T) {
	releaseErr := errors.New("release backend failed")
	inner := &recordingQuotaLease{err: releaseErr}
	first, second := startedBindingsForStore(t, &acquireQuotaStore{lease: inner, allowed: true})

	lease, allowed, err := first.Acquire(context.Background(), "consumer", 1, time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("Acquire() = %#v, %v, %v; want lease success", lease, allowed, err)
	}
	firstErr := lease.Release(context.Background())
	secondErr := lease.Release(context.Background())
	if firstErr != releaseErr || secondErr != releaseErr {
		t.Fatalf("Release() errors = %v, %v; want stable %v", firstErr, secondErr, releaseErr)
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("underlying Release calls = %d, want 1", calls)
	}
	if first.Ready() || second.Ready() {
		t.Fatal("shared bindings remained ready after lease release backend failure")
	}
}

func TestStateBindingRejectsNilSuccessfulLeaseAndMarksSharedStateUnready(t *testing.T) {
	first, second := startedBindingsForStore(t, &acquireQuotaStore{allowed: true})

	lease, allowed, err := first.Acquire(context.Background(), "consumer", 1, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "empty lease") {
		t.Fatalf("Acquire() error = %v, want clear empty lease contract error", err)
	}
	if lease != nil || allowed {
		t.Fatalf("Acquire() = %#v, %v; nil lease must not be admission success", lease, allowed)
	}
	if first.Ready() || second.Ready() {
		t.Fatal("shared bindings remained ready after backend violated lease contract")
	}
}

func TestStateLeaseCanceledReleaseIsIdempotentWithoutMarkingBackendUnhealthy(t *testing.T) {
	inner := &recordingQuotaLease{err: context.Canceled}
	first, second := startedBindingsForStore(t, &acquireQuotaStore{lease: inner, allowed: true})
	lease, allowed, err := first.Acquire(context.Background(), "consumer", 1, time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("Acquire() = %#v, %v, %v; want lease success", lease, allowed, err)
	}
	if err := lease.Release(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release() error = %v, want context.Canceled", err)
	}
	if err := lease.Release(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Release() error = %v, want cached context.Canceled", err)
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("underlying Release calls = %d, want 1", calls)
	}
	if !first.Ready() || !second.Ready() {
		t.Fatal("context cancellation incorrectly marked shared backend unhealthy")
	}
}

func startedBindingsForStore(t *testing.T, store quota.Store) (*stateBinding, *stateBinding) {
	t.Helper()
	config := platformstate.Config{Kind: platformstate.KindMemory}
	resource := &stateResource{config: config, store: store}
	resource.healthy.Store(true)
	pool := newStatePool()
	pool.entries[config] = resource
	first := newStateBinding(pool, config)
	second := newStateBinding(pool, config)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Close(context.Background())
		_ = second.Close(context.Background())
	})
	return first, second
}

type acquireQuotaStore struct {
	lease   quota.Lease
	allowed bool
	err     error
}

func (*acquireQuotaStore) AdmitRequest(context.Context, string, []quota.RequestLimit) (bool, error) {
	return true, nil
}
func (*acquireQuotaStore) TokenValue(context.Context, string, time.Duration) (int64, error) {
	return 0, nil
}
func (*acquireQuotaStore) RecordTokens(context.Context, string, int64) error { return nil }
func (store *acquireQuotaStore) Acquire(context.Context, string, int64, time.Duration) (quota.Lease, bool, error) {
	return store.lease, store.allowed, store.err
}

type recordingQuotaLease struct {
	calls atomic.Int32
	err   error
}

func (lease *recordingQuotaLease) Release(context.Context) error {
	lease.calls.Add(1)
	return lease.err
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

func TestTelemetryBindingsReusePrometheusWhenOtherSignalConfigurationChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*telemetry.Config)
	}{
		{name: "logs", mutate: func(config *telemetry.Config) {
			config.Logs = telemetry.SignalConfig{Kind: schema.ExporterKindStdout}
		}},
		{name: "traces", mutate: func(config *telemetry.Config) {
			config.Traces = telemetry.SignalConfig{Kind: schema.ExporterKindStdout}
		}},
		{name: "retention", mutate: func(config *telemetry.Config) {
			config.LogsRetentionDays++
			config.MetricsRetentionDays++
			config.TracesRetentionDays++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_ = listener.Close()
			base := telemetry.Config{Metrics: telemetry.SignalConfig{
				Kind:   schema.ExporterKindPrometheus,
				Params: map[string]string{"listen": addr, "path": "/metrics"},
			}}
			changed := base
			test.mutate(&changed)
			pool := newTelemetryPool()
			first := newTelemetryBinding(pool, base)
			second := newTelemetryBinding(pool, changed)
			if err := first.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer first.Close(context.Background())
			if err := second.Start(context.Background()); err != nil {
				t.Fatalf("hot update changing %s rebound unchanged Prometheus listener: %v", test.name, err)
			}
			defer second.Close(context.Background())
			if first.metricResource() != second.metricResource() {
				t.Fatal("unchanged Prometheus MeterProvider/handler/listener was not reused")
			}
			switch test.name {
			case "logs":
				if first.logResource() == second.logResource() || second.logResource().config.Kind != schema.ExporterKindStdout {
					t.Fatal("changed logs configuration did not install the requested logs resource")
				}
			case "traces":
				if first.traceResource() == second.traceResource() || second.traceResource().config.Kind != schema.ExporterKindStdout {
					t.Fatal("changed traces configuration did not install the requested traces resource")
				}
			case "retention":
				if first.logResource() != second.logResource() || first.traceResource() != second.traceResource() {
					t.Fatal("retention-only update churned signal providers")
				}
			}
		})
	}
}

func TestRejectedTelemetryCandidateKeepsGlobalsAndLastKnownGood(t *testing.T) {
	protocols, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewLLMProviderCatalog()
	if err != nil {
		t.Fatal(err)
	}
	states := newStatePool()
	telemetryResources := newTelemetryPool()
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := newDefaultLLMFactory(protocols, providers, states, telemetryResources, nil)
	reconciler := NewReconciler(host, &GraphBuilder{Protocols: protocols, Providers: providers, LLMFactory: factory})
	t.Cleanup(func() {
		_ = host.Close(context.Background())
		_ = telemetryResources.Close(context.Background())
		_ = states.Close()
	})

	globalMeter := otel.GetMeterProvider()
	globalTracer := otel.GetTracerProvider()
	good := (&configsnapshot.Builder{}).Build()
	if err := reconciler.Apply(context.Background(), good, "good"); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var badBuilder configsnapshot.Builder
	badBuilder.SetSetting("candidate", "bad")
	badBuilder.SetSetting("obs_metrics_exporter", "prometheus")
	badBuilder.SetSetting("obs_metrics_prometheus_listen", listener.Addr().String())
	if err := reconciler.Apply(context.Background(), badBuilder.Build(), "bad"); err == nil {
		t.Fatal("candidate with conflicting Prometheus listener activated")
	}
	if otel.GetMeterProvider() != globalMeter || otel.GetTracerProvider() != globalTracer {
		t.Fatal("failed candidate or rollback mutated process-global OTel providers")
	}
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("last-known-good generation became unavailable")
	}
	defer lease.Release()
	if lease.Value().Snapshot != good {
		t.Fatal("failed telemetry candidate replaced last-known-good generation")
	}
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

func TestProviderTransportCachesByNormalizedProxyURLWithinGeneration(t *testing.T) {
	roundTripper := &lifecycleRoundTripper{}
	transport := newProviderTransport(llmruntime.SettingsFromSnapshot(nil), roundTripper)
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := []provider.Request{
		{Method: http.MethodGet, URL: "http://provider.example/direct"},
		{Method: http.MethodGet, URL: "http://provider.example/legacy", ProxyURL: "enabled"},
		{Method: http.MethodGet, URL: "http://provider.example/first", ProxyURL: "http://proxy.example:8080"},
		{Method: http.MethodGet, URL: "http://provider.example/first-again", ProxyURL: "http://proxy.example:8080"},
		{Method: http.MethodGet, URL: "http://provider.example/second", ProxyURL: "http://other-proxy.example:9090"},
	}
	instances := make([]*providerhttp.Transport, 0, len(requests))
	for _, request := range requests {
		response, err := transport.Do(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		transport.mu.Lock()
		instances = append(instances, transport.transports[providerhttp.NormalizeProxyURL(request.ProxyURL)])
		transport.mu.Unlock()
	}
	if instances[0] == nil || instances[0] != instances[1] {
		t.Fatal("direct and invalid proxy requests did not reuse the same normalized transport instance")
	}
	if instances[2] == nil || instances[2] != instances[3] {
		t.Fatal("repeated normalized proxy key did not reuse the same transport instance")
	}
	transport.mu.Lock()
	count := len(transport.transports)
	transport.mu.Unlock()
	if count != 3 {
		t.Fatalf("cached transports = %d, want direct plus two valid proxy URLs", count)
	}
	if err := transport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := roundTripper.closes.Load(); got != 3 {
		t.Fatalf("CloseIdleConnections calls = %d, want one per cached transport", got)
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

func TestDefaultLLMGenerationFailsRuntimeSourceClosedWhenStateBindingIsPoisoned(t *testing.T) {
	protocols, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewLLMProviderCatalog()
	if err != nil {
		t.Fatal(err)
	}
	stateConfig := platformstate.Config{Kind: platformstate.KindMemory}
	failedLease := &recordingQuotaLease{err: errors.New("state lease release failed")}
	stateResource := &stateResource{
		config: stateConfig,
		store:  &acquireQuotaStore{lease: failedLease, allowed: true},
	}
	stateResource.healthy.Store(true)
	stateResources := newStatePool()
	stateResources.entries[stateConfig] = stateResource
	telemetryResources := newTelemetryPool()
	factory := newDefaultLLMFactory(protocols, providers, stateResources, telemetryResources, nil)
	graph := &GraphBuilder{Protocols: protocols, Providers: providers, LLMFactory: factory}
	candidate, err := graph.Build(context.Background(), (&configsnapshot.Builder{}).Build(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	var binding *stateBinding
	for _, component := range candidate.Components {
		if component.ID != "state" {
			continue
		}
		guarded, ok := component.Lifecycle.(*onceLifecycle)
		if !ok {
			t.Fatalf("state lifecycle = %T, want *onceLifecycle", component.Lifecycle)
		}
		binding, ok = guarded.lifecycle.(*stateBinding)
		if !ok {
			t.Fatalf("guarded state lifecycle = %T, want *stateBinding", guarded.lifecycle)
		}
	}
	if binding == nil {
		t.Fatal("DefaultLLMFactory candidate has no state binding")
	}
	host := kernel.NewHost[*ApplicationRuntime]()
	t.Cleanup(func() {
		_ = host.Close(context.Background())
		_ = telemetryResources.Close(context.Background())
		_ = stateResources.Close()
	})
	if _, err := host.Activate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	source := NewRuntimeSource(host)
	if !source.Ready() {
		t.Fatal("RuntimeSource is not ready after activating healthy DefaultLLMFactory generation")
	}

	lease, allowed, err := binding.Acquire(context.Background(), "consumer", 1, time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("state binding Acquire() = %#v, %v, %v; want lease", lease, allowed, err)
	}
	if err := lease.Release(context.Background()); err == nil {
		t.Fatal("poisoning state lease Release() returned nil")
	}
	if source.Ready() {
		t.Fatal("RuntimeSource remained ready after its active state binding became unhealthy")
	}
	if runtime, release, ok := source.Acquire(); ok || runtime != nil || release != nil {
		t.Fatalf("RuntimeSource.Acquire() = %p, release present %v, ok %v after state failure", runtime, release != nil, ok)
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
