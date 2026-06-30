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

// TestDispatchEmbeddingsEndToEnd verifies the verbatim passthrough: the client
// sends alias "text-embedding", the gateway rewrites the model to the backend
// "text-embedding-3-small" and forwards the body; the upstream response is
// returned to the client verbatim.
func TestDispatchEmbeddingsEndToEnd(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		receivedModel, _ = obj["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,`+
			`"embedding":[0.1,0.2]}],"model":"`+receivedModel+`",`+
			`"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	st := memory.New()
	prov, _ := st.Providers().Create(storage.CreateProvider{
		Name: "emb", Protocol: "openai-compatible", BaseURL: upstream.URL, APIKey: "k",
	})
	_, _ = st.Models().Create(storage.CreateModel{
		Name:    "text-embedding",
		Targets: []storage.CreateModelBackend{{ProviderID: prov.ID, Model: "text-embedding-3-small"}},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, st.Storage()))

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
}
