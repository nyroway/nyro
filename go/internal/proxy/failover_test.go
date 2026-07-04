package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestDispatchFailover verifies the dispatcher retries the next backend when
// the first returns 5xx: priority-1 backend 500s, priority-2 backend 200s.
func TestDispatchFailover(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer up1.Close()
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up2.Close()

	st := memory.New()
	core := st.Storage()
	p1, _ := core.Upstreams().Create(storage.CreateUpstream{Name: "p1", Provider: "p1", Protocol: "openai-chatcompletions", BaseURL: up1.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	p2, _ := core.Upstreams().Create(storage.CreateUpstream{Name: "p2", Provider: "p2", Protocol: "openai-chatcompletions", BaseURL: up2.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", Balance: storage.BalancePriority,
		Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: p1.ID, Model: "gpt-4o", Priority: 1},
			{UpstreamID: p2.ID, Model: "gpt-4o", Priority: 2},
		},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, core))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d (expected failover to the 200 backend), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"ok"`) {
		t.Errorf("expected content from the second backend: %s", rec.Body.String())
	}
}

// TestFailoverPreservesClientModel verifies that when backend 1 rewrites the
// model and fails, a backend-2 wildcard target resolves to the CLIENT's
// original model, not backend 1's rewrite.
func TestFailoverPreservesClientModel(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer up1.Close()

	var gotModel string
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up2.Close()

	st := memory.New().Storage()
	p1, _ := st.Upstreams().Create(storage.CreateUpstream{Name: "p1", Provider: "p1", Protocol: "openai-chatcompletions", BaseURL: up1.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	p2, _ := st.Upstreams().Create(storage.CreateUpstream{Name: "p2", Provider: "p2", Protocol: "openai-chatcompletions", BaseURL: up2.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	_, _ = st.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", Balance: storage.BalancePriority,
		Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: p1.ID, Model: "p1-rewritten-model", Priority: 1},
			{UpstreamID: p2.ID, Model: "*", Priority: 2},
		},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, st))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotModel != "gpt-4o" {
		t.Errorf("backend 2 received model %q, want the client's original %q", gotModel, "gpt-4o")
	}
}
