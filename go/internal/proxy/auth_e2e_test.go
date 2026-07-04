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

func boolPtr(b bool) *bool { return &b }

// TestInboundAuthStatusCodes verifies the distinct deny codes: 401 missing/invalid,
// 403 disabled, 200 valid. Ported parity for proxy/dispatcher/auth.rs.
func TestInboundAuthStatusCodes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()

	st := memory.New()
	core := st.Storage()
	up2, _ := core.Upstreams().Create(storage.CreateUpstream{Name: "p", Provider: "p", Protocol: "openai-chatcompletions", BaseURL: up.URL, CredentialsJSON: []byte(`{"api_key":"k"}`)})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", EnableAuth: true,
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up2.ID, Model: "gpt-4o"}},
	})
	consumer, _ := core.Consumers().Create(storage.CreateConsumer{
		Name: "test", Keys: []storage.CreateConsumerKey{{Name: "primary"}}, Routes: []string{"gpt-4o"},
	})
	token := consumer.Keys[0].Token
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	engine := NewRouter(gw)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	post := func(tok string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := post(""); c != http.StatusUnauthorized {
		t.Errorf("no key → %d, want 401", c)
	}
	if c := post("bogus"); c != http.StatusUnauthorized {
		t.Errorf("invalid key → %d, want 401", c)
	}
	if c := post(token); c != http.StatusOK {
		t.Errorf("valid key → %d, want 200", c)
	}
	// disable the consumer → its keys are revoked too (FindKey ANDs the key's
	// own Enabled with the owning consumer's Enabled), not just individually
	// disabled keys.
	if _, err := core.Consumers().Update(consumer.ID, storage.UpdateConsumer{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable consumer: %v", err)
	}
	if err := gw.Cache.LoadAndSwap(core); err != nil { // reflect the storage change in the in-memory cache
		t.Fatalf("reload cache: %v", err)
	}
	if c := post(token); c != http.StatusForbidden {
		t.Errorf("disabled consumer's key → %d, want 403", c)
	}
}
