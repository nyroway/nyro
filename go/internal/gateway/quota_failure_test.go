package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func TestQuotaStateErrorsReturn503AndLimitsReturn429(t *testing.T) {
	backendFailure := errors.New("backend unavailable")
	tests := []struct {
		name       string
		quotaType  string
		configure  func(*gatewayQuotaStore)
		wantStatus int
	}{
		{
			name:      "request admission backend failure",
			quotaType: "requests",
			configure: func(store *gatewayQuotaStore) {
				store.admitErr = backendFailure
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:      "token usage backend failure",
			quotaType: "tokens",
			configure: func(store *gatewayQuotaStore) {
				store.tokenValueErr = backendFailure
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:      "concurrency backend failure",
			quotaType: "concurrency",
			configure: func(store *gatewayQuotaStore) {
				store.acquireErr = backendFailure
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:      "request limit reached",
			quotaType: "requests",
			configure: func(store *gatewayQuotaStore) {
				store.admitDenied = true
			},
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:      "token limit reached",
			quotaType: "tokens",
			configure: func(store *gatewayQuotaStore) {
				store.tokenValue = 1
			},
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:      "concurrency limit reached",
			quotaType: "concurrency",
			configure: func(store *gatewayQuotaStore) {
				store.allowed = false
			},
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &gatewayQuotaStore{allowed: true}
			tt.configure(store)
			engine, key, _ := newQuotaTestGateway(t, store, tt.quotaType)
			rec := makeQuotaRequest(engine, key)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), "quota state unavailable") {
				t.Fatalf("503 body = %s", rec.Body.String())
			}
		})
	}
}

func TestAuthenticationFailureDoesNotCallQuotaStore(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true}
	engine, _, _ := newQuotaTestGateway(t, store, "requests")
	rec := makeQuotaRequest(engine, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	store.mu.Lock()
	calls := store.tokenValueCalls + store.recordTokensCalls + store.admitCalls + store.acquireCalls
	store.mu.Unlock()
	if calls != 0 {
		t.Fatalf("quota Store calls = %d, want 0", calls)
	}
}

func TestQuotaRecordFailureKeepsResponseAndMakesGatewayUnready(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true, recordTokensErr: errors.New("record failed")}
	engine, key, quotaSwitch := newQuotaTestGateway(t, store, "")
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if quotaSwitch.Ready() {
		t.Fatal("record failure did not mark State unhealthy")
	}
	store.mu.Lock()
	tokens := store.tokens
	store.mu.Unlock()
	if tokens != 5 {
		t.Fatalf("recorded tokens = %d, want 5", tokens)
	}
	assertGatewayUnready(t, engine)
}

func TestRequestQuotaAdmissionRunsAfterConcurrencyAcquire(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true}
	quotas := []storage.CreateConsumerQuota{
		{QuotaType: "tokens", QuotaLimit: 100, Window: "1m"},
		{QuotaType: "concurrency", QuotaLimit: 1},
		{QuotaType: "requests", QuotaLimit: 1, Window: "1m"},
	}
	engine, key, _, _ := newQuotaTestGatewayWithQuotas(t, store, quotas)
	if rec := makeQuotaRequest(engine, key); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	events := append([]string(nil), store.events...)
	store.mu.Unlock()
	wantPrefix := []string{"token-value", "concurrency-acquire", "request-admit"}
	if len(events) < len(wantPrefix) || !slices.Equal(events[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("events = %v, want prefix %v", events, wantPrefix)
	}
}

func TestRequestQuotaDenialReleasesConcurrencyLease(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true, admitDenied: true}
	quotas := []storage.CreateConsumerQuota{
		{QuotaType: "concurrency", QuotaLimit: 1},
		{QuotaType: "requests", QuotaLimit: 1, Window: "1m"},
	}
	engine, key, _, upstreamCalls := newQuotaTestGatewayWithQuotas(t, store, quotas)
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	releaseCalls := store.releaseCalls
	store.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", releaseCalls)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
	}
}

func TestRequestQuotaFailureReleasesLeaseAndReturns503(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true, admitErr: errors.New("redis failed")}
	quotas := []storage.CreateConsumerQuota{
		{QuotaType: "concurrency", QuotaLimit: 1},
		{QuotaType: "requests", QuotaLimit: 1, Window: "1m"},
	}
	engine, key, quotaSwitch, upstreamCalls := newQuotaTestGatewayWithQuotas(t, store, quotas)
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	releaseCalls := store.releaseCalls
	store.mu.Unlock()
	if releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", releaseCalls)
	}
	if quotaSwitch.Ready() {
		t.Fatal("admission failure kept State ready")
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
	}
}

func TestRequestQuotaReleaseFailureReturns503(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true, admitDenied: true, leaseErr: errors.New("release failed")}
	quotas := []storage.CreateConsumerQuota{
		{QuotaType: "concurrency", QuotaLimit: 1},
		{QuotaType: "requests", QuotaLimit: 1, Window: "1m"},
	}
	engine, key, quotaSwitch, upstreamCalls := newQuotaTestGatewayWithQuotas(t, store, quotas)
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if quotaSwitch.Ready() {
		t.Fatal("lease release failure kept State ready")
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
	}
}

func TestQuotaStageRecordsTokensOnly(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true}
	engine, key, _ := newQuotaTestGateway(t, store, "")
	if rec := makeQuotaRequest(engine, key); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	tokens := store.tokens
	store.mu.Unlock()
	if tokens != 5 {
		t.Fatalf("tokens = %d, want 5", tokens)
	}
}

func TestQuotaLeaseReleaseFailureKeepsResponseAndMakesGatewayUnready(t *testing.T) {
	store := &gatewayQuotaStore{
		allowed:  true,
		leaseErr: errors.New("release failed"),
	}
	engine, key, quotaSwitch := newQuotaTestGateway(t, store, "concurrency")
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if quotaSwitch.Ready() {
		t.Fatal("lease release failure did not mark State unhealthy")
	}
	assertGatewayUnready(t, engine)
}

func newQuotaTestGateway(t *testing.T, quotaStore quota.Store, quotaType string) (http.Handler, string, *quota.Switch) {
	t.Helper()
	var quotas []storage.CreateConsumerQuota
	if quotaType != "" {
		quotaSpec := storage.CreateConsumerQuota{QuotaType: quotaType, QuotaLimit: 1}
		if quotaType != "concurrency" {
			quotaSpec.Window = "1m"
		}
		quotas = append(quotas, quotaSpec)
	}
	engine, key, quotaSwitch, _ := newQuotaTestGatewayWithQuotas(t, quotaStore, quotas)
	return engine, key, quotaSwitch
}

func newQuotaTestGatewayWithQuotas(t *testing.T, quotaStore quota.Store, quotas []storage.CreateConsumerQuota) (http.Handler, string, *quota.Switch, *atomic.Int64) {
	t.Helper()
	upstreamCalls := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"r1\",\"object\":\"chat.completion\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}")
	}))
	t.Cleanup(upstream.Close)

	state := memory.New()
	core := state.Storage()
	up, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "test", Provider: "test", Protocol: "openai-chatcompletions",
		BaseURL: upstream.URL, CredentialsJSON: []byte("{\"api_key\":\"test-key\"}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = core.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", EnableAuth: true,
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "gpt-4o"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := core.Consumers().Create(storage.CreateConsumer{
		Name: "test", Keys: []storage.CreateConsumerKey{{Name: "primary"}},
		Routes: []string{"gpt-4o"}, Quotas: quotas,
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := &configsnapshot.Cache{}
	if err := cache.LoadAndSwap(core); err != nil {
		t.Fatal(err)
	}
	quotaSwitch := quota.NewSwitch(quotaStore)
	gateway := NewGatewayWithCache(cache, quotaSwitch)
	return NewRouter(gateway), consumer.Keys[0].Token, quotaSwitch, upstreamCalls
}

func makeQuotaRequest(handler http.Handler, key string) *httptest.ResponseRecorder {
	body := "{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertGatewayUnready(t *testing.T, handler http.Handler) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", rec.Code)
	}
}

type gatewayQuotaStore struct {
	mu                sync.Mutex
	tokenValue        int64
	tokenValueErr     error
	recordTokensErr   error
	acquireErr        error
	admitErr          error
	admitDenied       bool
	allowed           bool
	leaseErr          error
	tokens            int64
	events            []string
	tokenValueCalls   int
	recordTokensCalls int
	admitCalls        int
	acquireCalls      int
	releaseCalls      int
}

func (s *gatewayQuotaStore) AdmitRequest(context.Context, string, []quota.RequestLimit) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitCalls++
	s.events = append(s.events, "request-admit")
	if s.admitErr != nil {
		return false, s.admitErr
	}
	return !s.admitDenied, nil
}

func (s *gatewayQuotaStore) TokenValue(context.Context, string, time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenValueCalls++
	s.events = append(s.events, "token-value")
	return s.tokenValue, s.tokenValueErr
}

func (s *gatewayQuotaStore) RecordTokens(_ context.Context, _ string, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordTokensCalls++
	s.tokens = tokens
	return s.recordTokensErr
}

func (s *gatewayQuotaStore) Acquire(context.Context, string, int64, time.Duration) (quota.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	s.events = append(s.events, "concurrency-acquire")
	if s.acquireErr != nil {
		return nil, false, s.acquireErr
	}
	if !s.allowed {
		return nil, false, nil
	}
	return &gatewayQuotaLease{
		err: s.leaseErr,
		release: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.releaseCalls++
			s.events = append(s.events, "concurrency-release")
		},
	}, true, nil
}

type gatewayQuotaLease struct {
	once    sync.Once
	err     error
	release func()
}

func (l *gatewayQuotaLease) Release(context.Context) error {
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
	return l.err
}
