package httpingress_test

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

// TestDispatchEmbeddingsEndToEnd verifies the approved opaque passthrough: the
// client alias is rewritten for the Provider while only the response status,
// Content-Type, and body cross back through the HTTP ingress boundary.
func TestDispatchEmbeddingsEndToEnd(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		receivedModel, _ = obj["model"].(string)
		w.Header().Set("Content-Type", "application/provider+json")
		w.Header().Set("Set-Cookie", "provider_session=secret")
		w.Header().Set("Authorization", "Bearer upstream-secret")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Proxy-Authenticate", "Basic realm=provider")
		w.Header().Set("X-Nyro-Internal", "provider-only")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,`+
			`"embedding":[0.1,0.2]}],"model":"`+receivedModel+`",`+
			`"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	st := memory.New()
	core := st.Storage()
	up, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "emb", Protocol: "openai-embeddings", BaseURL: upstream.URL,
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model:     "text-embedding",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "text-embedding-3-small"}},
	})
	engine := newTestHandler(t, newTestSourceFromStorage(t, core))

	body := `{"model":"text-embedding","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if receivedModel != "text-embedding-3-small" {
		t.Errorf("upstream received model=%q, want text-embedding-3-small", receivedModel)
	}
	if !strings.Contains(rec.Body.String(), `"embedding":[0.1,0.2]`) {
		t.Errorf("response not verbatim passthrough:\n%s", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/provider+json" || len(rec.Header()) != 1 {
		t.Errorf("client response headers = %#v, want only Provider Content-Type", rec.Header())
	}
}
