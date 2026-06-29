package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
)

// statusRecorder wraps http.ResponseWriter to capture the response status for
// audit logging. It forwards Flush so SSE streaming works through it.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// appendLog writes a request-audit row (drives the rpm/rpd quota counters and
// the admin log/stats views). Token accounting is wired when usage is captured
// end-to-end; for now input/output are 0.
func (g *Gateway) appendLog(model storage.Model, provider storage.Provider, apiKeyID string, started time.Time, status int, usage ir.Usage) {
	code := int32(status)
	latency := time.Since(started).Milliseconds()
	entry := storage.RequestLog{
		ID:               newRequestID(),
		CreatedAt:        started.UnixMilli(),
		APIKeyID:         apiKeyID,
		ModelID:          model.ID,
		ModelName:        model.Name,
		ProviderID:       provider.ID,
		ProviderName:     provider.Name,
		ClientStatusCode: &code,
		LatencyTotalMs:   &latency,
		InputTokens:      int32(usage.PromptTokens),
		OutputTokens:     int32(usage.CompletionTokens),
	}
	_ = g.Storage.Logs().AppendBatch([]storage.RequestLog{entry})
}

func newRequestID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "req_" + hex.EncodeToString(buf[:])
}
