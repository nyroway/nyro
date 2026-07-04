package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestModelsList verifies GET /v1/models (cutover blocker B2): open routes
// (enable_auth=false) are always listed; auth-gated routes appear only when the
// caller presents a valid key granted access to them. Output is the
// OpenAI-compatible {object:"list", data:[{id,object:"model",created:0,owned_by:"Nyro"}]}.
func TestModelsList(t *testing.T) {
	st := memory.New()
	core := st.Storage()
	up, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "p", Provider: "p", Protocol: "openai-chatcompletions", BaseURL: "http://up",
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	if _, err := core.Routes().Create(storage.CreateRoute{
		Model:     "open-model",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "open-model"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Routes().Create(storage.CreateRoute{
		Model:      "secret-model",
		EnableAuth: true,
		Upstreams:  []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "secret-model"}},
	}); err != nil {
		t.Fatal(err)
	}
	consumer, err := core.Consumers().Create(storage.CreateConsumer{
		Name:   "k1",
		Keys:   []storage.CreateConsumerKey{{Name: "primary"}},
		Routes: []string{"secret-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := consumer.Keys[0].Token

	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	r := NewRouter(gw)

	// No API key → only open routes.
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
		t.Errorf("no key: secret-model leaked without a granted key; body %s", rec.Body.String())
	}

	// Granted API key → open + secret.
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
