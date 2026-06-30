package proxy

import (
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
	p1, _ := st.Providers().Create(storage.CreateProvider{Name: "p1", Protocol: "openai-compatible", BaseURL: up1.URL, APIKey: "k"})
	p2, _ := st.Providers().Create(storage.CreateProvider{Name: "p2", Protocol: "openai-compatible", BaseURL: up2.URL, APIKey: "k"})
	_, _ = st.Models().Create(storage.CreateModel{
		Name: "gpt-4o", Balance: storage.BalancePriority,
		Targets: []storage.CreateModelBackend{
			{ProviderID: p1.ID, Model: "gpt-4o", Priority: 1},
			{ProviderID: p2.ID, Model: "gpt-4o", Priority: 2},
		},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, st.Storage()))

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
