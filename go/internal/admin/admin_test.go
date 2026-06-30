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
	if rec := do(r, "GET", "/api/v1/providers", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → %d, want 401", rec.Code)
	}
	if rec := do(r, "GET", "/api/v1/providers", "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token → %d, want 401", rec.Code)
	}
	if rec := do(r, "GET", "/api/v1/status", "secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("status with token → %d", rec.Code)
	}
}

func TestAdminProviderCRUD(t *testing.T) {
	r, _ := newEngine(t, "secret")

	rec := do(r, "POST", "/api/v1/providers", "secret",
		[]byte(`{"name":"OpenAI","protocol":"openai-compatible","base_url":"https://api.openai.com","api_key":"sk-1"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var p storage.Provider
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.AuthMode != "apikey" || !p.IsEnabled {
		t.Errorf("provider: %+v", p)
	}

	rec = do(r, "GET", "/api/v1/providers", "secret", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"OpenAI"`)) {
		t.Errorf("list → %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminModelAndSettings(t *testing.T) {
	r, st := newEngine(t, "") // empty token disables auth
	prov, _ := st.Providers().Create(storage.CreateProvider{
		Name: "P", Protocol: "openai-compatible", BaseURL: "u", APIKey: "k",
	})

	rec := do(r, "POST", "/api/v1/models", "",
		[]byte(`{"name":"gpt-4o","targets":[{"provider_id":"`+prov.ID+`","model":"gpt-4o"}]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model → %d %s", rec.Code, rec.Body.String())
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
