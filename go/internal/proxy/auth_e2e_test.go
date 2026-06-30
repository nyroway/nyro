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
	prov, _ := st.Providers().Create(storage.CreateProvider{Name: "p", Protocol: "openai-compatible", BaseURL: up.URL, APIKey: "k"})
	m, _ := st.Models().Create(storage.CreateModel{
		Name: "gpt-4o", EnableAuth: true,
		Targets: []storage.CreateModelBackend{{ProviderID: prov.ID, Model: "gpt-4o"}},
	})
	key, _ := st.APIKeys().Create(storage.CreateApiKey{Name: "test", ModelIDs: []string{m.ID}})
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	engine := NewRouter(gw)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
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
	if c := post(key.Token); c != http.StatusOK {
		t.Errorf("valid key → %d, want 200", c)
	}
	// disable the key → 403
	if _, err := st.APIKeys().Update(key.ID, storage.UpdateApiKey{IsEnabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable key: %v", err)
	}
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil { // reflect the storage change in the in-memory cache
		t.Fatalf("reload cache: %v", err)
	}
	if c := post(key.Token); c != http.StatusForbidden {
		t.Errorf("disabled key → %d, want 403", c)
	}
}
