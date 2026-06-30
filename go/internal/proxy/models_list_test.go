package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestModelsList verifies GET /v1/models (cutover blocker B2): open models
// (enable_auth=false) are always listed; auth-gated models appear only when the
// caller presents a valid API key bound to them. Output is the OpenAI-compatible
// {object:"list", data:[{id,object:"model",created:0,owned_by:"Nyro"}]}.
func TestModelsList(t *testing.T) {
	st := memory.New()
	prov, _ := st.Providers().Create(storage.CreateProvider{
		Name: "p", Protocol: "openai-compatible", BaseURL: "http://up", APIKey: "k",
	})
	if _, err := st.Models().Create(storage.CreateModel{
		Name:    "open-model",
		Targets: []storage.CreateModelBackend{{ProviderID: prov.ID, Model: "open-model"}},
	}); err != nil {
		t.Fatal(err)
	}
	secret, err := st.Models().Create(storage.CreateModel{
		Name:       "secret-model",
		EnableAuth: true,
		Targets:    []storage.CreateModelBackend{{ProviderID: prov.ID, Model: "secret-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.APIKeys().Create(storage.CreateApiKey{
		Name:     "k1",
		ModelIDs: []string{secret.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	r := NewRouter(gw)

	// No API key → only open models.
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no key: status %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"open-model"`) {
		t.Errorf("no key: open-model missing; body %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-model") {
		t.Errorf("no key: secret-model leaked without a bound key; body %s", rec.Body.String())
	}

	// Bound API key → open + secret.
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key.Token)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with key: status %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"open-model"`) || !strings.Contains(body, `"id":"secret-model"`) {
		t.Errorf("with key: expected both models; body %s", body)
	}
	// OpenAI-compatible envelope.
	if !strings.Contains(body, `"object":"list"`) || !strings.Contains(body, `"owned_by":"Nyro"`) {
		t.Errorf("with key: bad envelope; body %s", body)
	}
}
