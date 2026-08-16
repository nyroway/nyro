package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
			name:      "usage backend failure",
			quotaType: "requests",
			configure: func(store *gatewayQuotaStore) {
				store.valueErr = backendFailure
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
			name:      "usage limit reached",
			quotaType: "requests",
			configure: func(store *gatewayQuotaStore) {
				store.value = 1
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
	calls := store.valueCalls + store.recordCalls + store.acquireCalls
	store.mu.Unlock()
	if calls != 0 {
		t.Fatalf("quota Store calls = %d, want 0", calls)
	}
}

func TestQuotaRecordFailureKeepsResponseAndMakesGatewayUnready(t *testing.T) {
	store := &gatewayQuotaStore{allowed: true, recordErr: errors.New("record failed")}
	engine, key, quotaSwitch := newQuotaTestGateway(t, store, "")
	rec := makeQuotaRequest(engine, key)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if quotaSwitch.Ready() {
		t.Fatal("record failure did not mark State unhealthy")
	}
	store.mu.Lock()
	usage := store.usage
	store.mu.Unlock()
	if usage != (quota.Usage{Requests: 1, Tokens: 5}) {
		t.Fatalf("recorded usage = %+v, want requests=1 tokens=5", usage)
	}
	assertGatewayUnready(t, engine)
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	var quotas []storage.CreateConsumerQuota
	if quotaType != "" {
		quotaSpec := storage.CreateConsumerQuota{QuotaType: quotaType, QuotaLimit: 1}
		if quotaType != "concurrency" {
			quotaSpec.Window = "1m"
		}
		quotas = append(quotas, quotaSpec)
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
	return NewRouter(gateway), consumer.Keys[0].Token, quotaSwitch
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
	mu           sync.Mutex
	value        int64
	valueErr     error
	recordErr    error
	acquireErr   error
	allowed      bool
	leaseErr     error
	usage        quota.Usage
	valueCalls   int
	recordCalls  int
	acquireCalls int
}

func (s *gatewayQuotaStore) AdmitRequest(context.Context, string, []quota.RequestLimit) (bool, error) {
	return true, nil
}

func (s *gatewayQuotaStore) Value(context.Context, string, string, time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valueCalls++
	return s.value, s.valueErr
}

func (s *gatewayQuotaStore) Record(_ context.Context, _ string, usage quota.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCalls++
	s.usage = usage
	return s.recordErr
}

func (s *gatewayQuotaStore) Acquire(context.Context, string, int64, time.Duration) (quota.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	if s.acquireErr != nil {
		return nil, false, s.acquireErr
	}
	if !s.allowed {
		return nil, false, nil
	}
	return &gatewayQuotaLease{err: s.leaseErr}, true, nil
}

type gatewayQuotaLease struct {
	once sync.Once
	err  error
}

func (l *gatewayQuotaLease) Release(context.Context) error {
	l.once.Do(func() {})
	return l.err
}
