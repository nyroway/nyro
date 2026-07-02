package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func newEngine(t *testing.T, token string) (chi.Router, *memory.Backend) {
	t.Helper()
	st := memory.New()
	r := chi.NewRouter()
	Mount(r, st.Storage(), token, nil, nil)
	return r, st
}

func do(r http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAdminAuth(t *testing.T) {
	r, _ := newEngine(t, "secret")
	if rec := do(r, "GET", "/api/v1/upstreams", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → %d, want 401", rec.Code)
	}
	if rec := do(r, "GET", "/api/v1/upstreams", "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token → %d, want 401", rec.Code)
	}
	if rec := do(r, "GET", "/api/v1/status", "secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("status with token → %d", rec.Code)
	}
}

func TestAdminUpstreamCRUD(t *testing.T) {
	r, _ := newEngine(t, "secret")

	rec := do(r, "POST", "/api/v1/upstreams", "secret",
		[]byte(`{"name":"OpenAI","provider":"openai","protocol":"openai-compatible","base_url":"https://api.openai.com","credentials":{"api_key":"sk-1"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || !u.Enabled {
		t.Errorf("upstream: %+v", u)
	}

	rec = do(r, "GET", "/api/v1/upstreams", "secret", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"OpenAI"`)) {
		t.Errorf("list → %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminRouteAndSettings(t *testing.T) {
	r, st := newEngine(t, "") // empty token disables auth
	up, _ := st.Storage().Upstreams().Create(storage.CreateUpstream{
		Name: "P", Provider: "p", Protocol: "openai-compatible", BaseURL: "u", CredentialsJSON: []byte(`{"api_key":"k"}`),
	})

	rec := do(r, "POST", "/api/v1/routes", "",
		[]byte(`{"model":"gpt-4o","upstreams":[{"upstream_id":"`+up.ID+`","model":"gpt-4o"}]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create route → %d %s", rec.Code, rec.Body.String())
	}

	rec = do(r, "PUT", "/api/v1/settings/log_retention_days", "", []byte(`{"value":"7"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("set → %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, "GET", "/api/v1/settings/log_retention_days", "", nil)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"value":"7"`)) {
		t.Errorf("get setting → %s", rec.Body.String())
	}
}

// TestAdminConsumerCreateExposesRawToken proves the create response carries the
// one-time plaintext token for each generated key (never persisted, never
// returned again on subsequent reads).
func TestAdminConsumerCreateExposesRawToken(t *testing.T) {
	r, _ := newEngine(t, "")

	rec := do(r, "POST", "/api/v1/consumers", "",
		[]byte(`{"name":"acme","keys":[{"name":"primary"}]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create consumer → %d %s", rec.Code, rec.Body.String())
	}
	var c storage.Consumer
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Keys) != 1 || c.Keys[0].Token == "" {
		t.Fatalf("consumer keys missing raw token: %+v", c.Keys)
	}

	// Re-fetch via list — the raw token must NOT be exposed again.
	rec = do(r, "GET", "/api/v1/consumers", "", nil)
	var list []storage.Consumer
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Keys) != 1 || list[0].Keys[0].Token != "" {
		t.Errorf("list should not expose raw token: %+v", list)
	}
}
