package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestConcurrencyQuotaEnforced verifies a consumer whose only quota is
// {QuotaType: "concurrency", QuotaLimit: 1} gets a second concurrent request
// rejected with 429 while the first is in flight, and that the slot is
// released once the first request completes.
func TestConcurrencyQuotaEnforced(t *testing.T) {
	blocker := make(chan struct{})
	reachedUpstream := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case reachedUpstream <- struct{}{}:
		default:
		}
		<-blocker
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()

	st := memory.New()
	core := st.Storage()
	p, _ := core.Upstreams().Create(storage.CreateUpstream{Name: "p", Provider: "p", Protocol: "openai-chatcompletions", BaseURL: up.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", EnableAuth: true,
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: p.ID, Model: "gpt-4o"}},
	})
	consumer, _ := core.Consumers().Create(storage.CreateConsumer{
		Name:   "test",
		Keys:   []storage.CreateConsumerKey{{Name: "primary"}},
		Routes: []string{"gpt-4o"},
		Quotas: []storage.CreateConsumerQuota{{QuotaType: "concurrency", QuotaLimit: 1}},
	})
	rawKey := consumer.Keys[0].Token

	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	engine := NewRouter(gw)

	do := func() *httptest.ResponseRecorder {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- do() }()

	// Wait until the first request has acquired the slot and reached the
	// upstream (deterministic — avoids racing the probe below for the slot).
	select {
	case <-reachedUpstream:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached the upstream")
	}

	if rec2 := do(); rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second concurrent request status=%d, want 429 (slot held by first)", rec2.Code)
	}

	close(blocker) // let the first request finish
	if rec1 := <-firstDone; rec1.Code != http.StatusOK {
		t.Fatalf("first request status=%d", rec1.Code)
	}
	if rec3 := do(); rec3.Code != http.StatusOK {
		t.Errorf("request after release status=%d, want 200 (slot released)", rec3.Code)
	}
}
