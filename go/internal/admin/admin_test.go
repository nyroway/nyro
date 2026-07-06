package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
		[]byte(`{"name":"OpenAI","provider":"openai","protocol":"openai-chatcompletions","base_url":"https://api.openai.com","credentials":{"api_key":"sk-1"}}`))
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
		Name: "P", Protocol: "openai-chatcompletions", BaseURL: "u", CredentialsJSON: []byte(`{"api_key":"k"}`),
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

// TestProtocolCredentials verifies the /protocol-credentials endpoint exposes
// exactly the four codec-backed protocols, each with the api_key field
// AuthenticatorFor requires.
func TestProtocolCredentials(t *testing.T) {
	r, _ := newEngine(t, "")

	rec := do(r, "GET", "/api/v1/protocol-credentials", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get protocol-credentials → %d %s", rec.Code, rec.Body.String())
	}
	var out []protocolCredentialsView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(out), out)
	}
	var anthropic *protocolCredentialsView
	for i := range out {
		if out[i].Protocol == "anthropic-messages" {
			anthropic = &out[i]
		}
	}
	if anthropic == nil {
		t.Fatalf("no anthropic-messages entry: %+v", out)
	}
	if len(anthropic.Fields) != 1 || anthropic.Fields[0].Name != "api_key" || !anthropic.Fields[0].Required {
		t.Errorf("anthropic-messages fields = %+v, want one required api_key field", anthropic.Fields)
	}
}

// TestMutationsBumpEpoch verifies every config-mutating endpoint bumps
// config_epoch so the xDS broadcaster pushes a fresh snapshot.
func TestMutationsBumpEpoch(t *testing.T) {
	r, st := newEngine(t, "")
	core := st.Storage()

	epoch := func() string {
		v, _ := core.Settings().Get("config_epoch")
		return v
	}

	up, err := core.Upstreams().Create(storage.CreateUpstream{Name: "u1", Protocol: "openai-chatcompletions", BaseURL: "https://api.openai.com/v1", CredentialsJSON: []byte(`{"api_key":"k"}`)})
	if err != nil {
		t.Fatalf("seed upstream: %v", err)
	}
	rt, err := core.Routes().Create(storage.CreateRoute{Model: "m1"})
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}

	steps := []struct {
		name, method, path, body string
	}{
		{"create upstream", "POST", "/api/v1/upstreams", `{"name":"u2","provider":"openai","credentials":{"api_key":"k"}}`},
		{"update upstream", "PUT", "/api/v1/upstreams/" + up.ID, `{"enabled":false}`},
		{"update route", "PUT", "/api/v1/routes/" + rt.ID, `{"enabled":false}`},
		{"delete route", "DELETE", "/api/v1/routes/" + rt.ID, ""},
		{"delete upstream", "DELETE", "/api/v1/upstreams/" + up.ID, ""},
	}
	for _, s := range steps {
		before := epoch()
		var body []byte
		if s.body != "" {
			body = []byte(s.body)
		}
		rec := do(r, s.method, s.path, "", body)
		if rec.Code >= 400 {
			t.Fatalf("%s: status %d %s", s.name, rec.Code, rec.Body.String())
		}
		if after := epoch(); after == before {
			t.Errorf("%s: config_epoch unchanged (%q) — gateways will not be notified", s.name, after)
		}
	}
}

func TestTestHTTPClientProxyRouting(t *testing.T) {
	direct := testHTTPClient("", 5*time.Second)
	if direct.Transport != nil {
		t.Errorf("empty proxyURL: want default transport (nil), got %T", direct.Transport)
	}

	invalid := testHTTPClient("enabled", 5*time.Second) // legacy pre-fix sentinel, not a real URL
	if invalid.Transport != nil {
		t.Errorf("invalid proxyURL: want default transport (nil, i.e. no proxy), got %T", invalid.Transport)
	}

	proxied := testHTTPClient("http://proxy.example:8080", 5*time.Second)
	tr, ok := proxied.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("valid proxyURL: transport is %T, want *http.Transport", proxied.Transport)
	}
	if tr.Proxy == nil {
		t.Error("valid proxyURL: transport has no Proxy function")
	}
}

// TestUpstreamModelsManual verifies a manual models_json list is returned
// directly by /upstreams/{id}/models with no HTTP call involved.
func TestUpstreamModelsManual(t *testing.T) {
	r, _ := newEngine(t, "")

	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m1","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models":["m1","m2"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models → %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 2 || out.Models[0] != "m1" || out.Models[1] != "m2" {
		t.Errorf("models = %+v, want [m1 m2]", out.Models)
	}
}

// TestUpstreamModelsCustomNoDiscovery verifies a custom upstream with neither
// models_url nor a static models list returns 200 with an empty list, not an
// error.
func TestUpstreamModelsCustomNoDiscovery(t *testing.T) {
	r, _ := newEngine(t, "")

	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m2","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models → %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 0 {
		t.Errorf("models = %+v, want empty", out.Models)
	}
}

// TestUpstreamModelsLiveDiscoveryCached verifies live discovery hits the
// upstream's models_url once and serves the second request from the TTL
// cache (no second HTTP round-trip).
func TestUpstreamModelsLiveDiscoveryCached(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer ts.Close()

	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m3","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models_url":"`+ts.URL+`/models"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("models call %d → %d %s", i, rec.Code, rec.Body.String())
		}
		var out struct {
			Models []string `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Models) != 2 || out.Models[0] != "m-a" || out.Models[1] != "m-b" {
			t.Errorf("call %d models = %+v, want [m-a m-b]", i, out.Models)
		}
	}
	if hits != 1 {
		t.Errorf("upstream discovery hits = %d, want 1 (second call should be cached)", hits)
	}
}

// TestUpstreamModelsDiscoveryHTTPErrorSurfaced verifies a non-2xx discovery
// response (e.g. an auth failure) is surfaced as an error rather than
// silently decoding to an empty model list and being cached as a success —
// a body with no "data" array (which a 401/403 error page commonly has)
// must not be mistaken for "zero models available".
func TestUpstreamModelsDiscoveryHTTPErrorSurfaced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer ts.Close()

	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m7","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models_url":"`+ts.URL+`/models"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models → %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Errorf("expected a non-empty error for a 401 discovery response, got %+v", out)
	}
	if len(out.Models) != 0 {
		t.Errorf("models = %+v, want empty on discovery error", out.Models)
	}
}

// TestUpstreamModelsCacheInvalidatedOnUpdate verifies that updating an
// upstream's models_url invalidates the previously cached discovery result,
// so the new endpoint's models are served (not the stale cached ones).
func TestUpstreamModelsCacheInvalidatedOnUpdate(t *testing.T) {
	var hits1, hits2 int
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits1++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"old-a"}]}`))
	}))
	defer ts1.Close()
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"new-a"}]}`))
	}))
	defer ts2.Close()

	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m4","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models_url":"`+ts1.URL+`/models"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	// Prime the cache against ts1.
	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models (priming) → %d %s", rec.Code, rec.Body.String())
	}
	if hits1 != 1 {
		t.Fatalf("hits1 = %d, want 1 after priming", hits1)
	}

	// Update models_url to point at ts2 — must invalidate the cache.
	rec = do(r, "PUT", "/api/v1/upstreams/"+u.ID, "", []byte(`{"models_url":"`+ts2.URL+`/models"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update → %d %s", rec.Code, rec.Body.String())
	}

	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models (post-update) → %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 1 || out.Models[0] != "new-a" {
		t.Errorf("post-update models = %+v, want [new-a]", out.Models)
	}
	if hits2 != 1 {
		t.Errorf("hits2 = %d, want 1 (stale ts1 cache entry must not leak into this response)", hits2)
	}
}

// TestCreateUpstreamRejectsInvalidFields verifies the admin REST API enforces
// the same invariants as the YAML config-loading path (see
// go/docs/schema/config.md): provider is required and must resolve to a
// known preset or "custom", models/models_url are mutually exclusive, and
// "custom" requires an explicit base_url.
func TestCreateUpstreamRejectsInvalidFields(t *testing.T) {
	r, _ := newEngine(t, "")
	cases := []struct {
		name string
		body string
	}{
		{"missing provider", `{"name":"a","credentials":{"api_key":"k"}}`},
		{"unknown provider", `{"name":"a","provider":"nope","credentials":{"api_key":"k"}}`},
		{"models and models_url both set", `{"name":"a","provider":"openai","models":["m1"],"models_url":"https://x/models","credentials":{"api_key":"k"}}`},
		{"custom missing base_url", `{"name":"a","provider":"custom","credentials":{"api_key":"k"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, "POST", "/api/v1/upstreams", "", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("create → %d %s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestUpdateUpstreamRejectsModelsMutualExclusion verifies an update that
// would leave both models and models_url set on the merged (existing +
// incoming) state is rejected, even when only one of the two fields is
// present in the update payload.
func TestUpdateUpstreamRejectsModelsMutualExclusion(t *testing.T) {
	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m5","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models":["m1"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	// Setting models_url without clearing the existing static `models` list
	// must be rejected, not silently leave both set.
	rec = do(r, "PUT", "/api/v1/upstreams/"+u.ID, "", []byte(`{"models_url":"https://x/models"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update → %d %s, want 400 (models + models_url would both be set)", rec.Code, rec.Body.String())
	}
}

// TestUpdateUpstreamClearsModelsWhenSwitchingToDiscovery verifies that
// sending an empty `models` array (as the WebUI does when switching a
// static-list upstream to URL discovery — see go-adapter.ts's
// updateUpstreamFromProvider) actually clears models_json, rather than
// persisting the literal empty-array string "[]" (which would read back as
// "a static list is still present" and shadow the new models_url both in
// modelsForUpstream and in the mutual-exclusion check on the next update).
func TestUpdateUpstreamClearsModelsWhenSwitchingToDiscovery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"discovered-a"}]}`))
	}))
	defer ts.Close()

	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m6","provider":"custom","base_url":"https://x","credentials":{"api_key":"k"},"models":["m1","m2"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	// Switch to discovery: clear models (sent as []), set models_url.
	rec = do(r, "PUT", "/api/v1/upstreams/"+u.ID, "",
		[]byte(`{"models":[],"models_url":"`+ts.URL+`/models"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update → %d %s", rec.Code, rec.Body.String())
	}

	rec = do(r, "GET", "/api/v1/upstreams/"+u.ID+"/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models → %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 1 || out.Models[0] != "discovered-a" {
		t.Errorf("models = %+v, want [discovered-a] (the manual list should no longer shadow discovery)", out.Models)
	}
}

// TestUpstreamTestConnectivityUsesResolvedModelsURL verifies /test resolves
// modelsDiscoveryURL (upstream's own models_url, here overriding openai's
// real preset URL) instead of guessing a path from protocol+base_url, and
// doesn't escape to the real internet for a known preset.
func TestUpstreamTestConnectivityUsesResolvedModelsURL(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	r, _ := newEngine(t, "")
	rec := do(r, "POST", "/api/v1/upstreams", "",
		[]byte(`{"name":"m5","provider":"openai","protocol":"openai-chatcompletions","base_url":"`+ts.URL+`","credentials":{"api_key":"k"},"models_url":"`+ts.URL+`/custom-models"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create → %d %s", rec.Code, rec.Body.String())
	}
	var u storage.Upstream
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}

	rec = do(r, "POST", "/api/v1/upstreams/"+u.ID+"/test", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("test → %d %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/custom-models" {
		t.Errorf("test hit path %q, want /custom-models (resolved models_url, not a guessed base_url+/models path)", gotPath)
	}
}
