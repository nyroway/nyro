package admin

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// TestStatsEndpoints verifies the Rust-aligned stats surface (cutover blocker
// B5): /stats/models + /stats/providers (renamed from by-*), the new
// /stats/api-keys, and the ?hours window filter. The WebUI calls these paths.
func TestStatsEndpoints(t *testing.T) {
	r, st := newEngine(t, "")
	now := time.Now().UnixMilli()
	code2 := int32(200)
	code5 := int32(500)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "1", CreatedAt: now, ModelName: "gpt-4o", ProviderName: "OpenAI", APIKeyID: "k1", APIKeyName: "key-one", InputTokens: 10, OutputTokens: 5, ClientStatusCode: &code2},
		{ID: "2", CreatedAt: now, ModelName: "gpt-4o", ProviderName: "OpenAI", APIKeyID: "k1", APIKeyName: "key-one", InputTokens: 20, ClientStatusCode: &code5},
	}); err != nil {
		t.Fatal(err)
	}

	// /stats/models (renamed from /stats/by-model).
	rec := do(r, "GET", "/api/v1/stats/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/stats/models → %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"model":"gpt-4o"`)) {
		t.Errorf("/api/v1/stats/models missing gpt-4o; body: %s", rec.Body.String())
	}

	// old path removed.
	if rec := do(r, "GET", "/api/v1/stats/by-model", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/api/v1/stats/by-model → %d, want 404 (renamed)", rec.Code)
	}

	// /stats/providers.
	rec = do(r, "GET", "/api/v1/stats/providers", "", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"provider":"OpenAI"`)) {
		t.Errorf("/api/v1/stats/providers → %d %s", rec.Code, rec.Body.String())
	}

	// /stats/api-keys.
	rec = do(r, "GET", "/api/v1/stats/api-keys", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/stats/api-keys → %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"api_key_name":"key-one"`)) {
		t.Errorf("/api/v1/stats/api-keys missing key-one; body: %s", rec.Body.String())
	}

	// ?hours filters out old logs.
	old := now - 48*60*60*1000
	st.Logs().AppendBatch([]storage.RequestLog{{ID: "3", CreatedAt: old, ModelName: "old-model", ProviderName: "Old", InputTokens: 1, ClientStatusCode: &code2}})
	rec = do(r, "GET", "/api/v1/stats/models?hours=1", "", nil)
	if bytes.Contains(rec.Body.Bytes(), []byte("old-model")) {
		t.Errorf("?hours=1 should exclude old-model; body: %s", rec.Body.String())
	}
}
