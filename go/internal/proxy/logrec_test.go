package proxy

import (
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestAppendLogPopulatesFields verifies appendLog fills the previously-empty
// RequestLog columns from the per-request logCtx + usage (cutover blocker B6,
// batch 1): api_key_name, client/upstream protocol, client/upstream model,
// method, path, upstream_status_code, and latency_upstream_ms.
func TestAppendLogPopulatesFields(t *testing.T) {
	st := memory.New()
	gw := NewGateway(st.Storage())
	upStatus := int32(200)
	upLatency := int64(42)
	lc := logCtx{
		apiKeyName:        "my-key",
		clientProtocol:    "openai_chat_completions",
		upstreamProtocol:  "openai_chat_completions",
		clientModel:       "gpt-4o",
		upstreamModel:     "gpt-4o",
		method:            "POST",
		path:              "/v1/chat/completions",
		upstreamStatus:    &upStatus,
		latencyUpstreamMs: &upLatency,
	}
	usage := ir.Usage{PromptTokens: 10, CompletionTokens: 5}
	gw.appendLog(storage.Model{ID: "m1", Name: "gpt-4o"}, storage.Provider{ID: "p1", Name: "OpenAI"},
		"key-id", time.Now(), 200, usage, lc)

	page, err := st.Logs().Query(storage.LogQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("expected 1 log, got %d err=%v", len(page.Items), err)
	}
	l := page.Items[0]
	for name, got := range map[string]string{
		"api_key_name":      l.APIKeyName,
		"client_protocol":   l.ClientProtocol,
		"upstream_protocol": l.UpstreamProtocol,
		"client_model":      l.ClientModel,
		"upstream_model":    l.UpstreamModel,
		"method":            l.Method,
		"path":              l.Path,
	} {
		if got == "" {
			t.Errorf("%s not populated", name)
		}
	}
	if l.UpstreamStatusCode == nil || *l.UpstreamStatusCode != 200 {
		t.Errorf("upstream_status_code = %v, want 200", l.UpstreamStatusCode)
	}
	if l.LatencyUpstreamMs == nil || *l.LatencyUpstreamMs != 42 {
		t.Errorf("latency_upstream_ms = %v, want 42", l.LatencyUpstreamMs)
	}
}
