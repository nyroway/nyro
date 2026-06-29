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

// logCtx carries the per-request fields captured across the dispatch lifecycle
// (protocols, models, method/path, upstream status/latency) so appendLog can
// populate the full RequestLog row. Mirrors the Rust LogBuilder.
type logCtx struct {
	apiKeyName        string
	clientProtocol    string
	upstreamProtocol  string
	clientModel       string
	upstreamModel     string
	method            string
	path              string
	isStream          bool
	upstreamStatus    *int32
	latencyUpstreamMs *int64
}

// appendLog writes a request-audit row (drives the rpm/rpd quota counters and
// the admin log/stats views). Populates the full column set from the captured
// logCtx + usage.
func (g *Gateway) appendLog(model storage.Model, provider storage.Provider, apiKeyID string, started time.Time, status int, usage ir.Usage, lc logCtx) {
	code := int32(status)
	latency := time.Since(started).Milliseconds()
	entry := storage.RequestLog{
		ID:                 newRequestID(),
		CreatedAt:          started.UnixMilli(),
		APIKeyID:           apiKeyID,
		APIKeyName:         lc.apiKeyName,
		ClientProtocol:     lc.clientProtocol,
		UpstreamProtocol:   lc.upstreamProtocol,
		ModelID:            model.ID,
		ModelName:          model.Name,
		ProviderID:         provider.ID,
		ProviderName:       provider.Name,
		ClientModel:        lc.clientModel,
		UpstreamModel:      lc.upstreamModel,
		Method:             lc.method,
		Path:               lc.path,
		ClientStatusCode:   &code,
		UpstreamStatusCode: lc.upstreamStatus,
		LatencyTotalMs:     &latency,
		LatencyUpstreamMs:  lc.latencyUpstreamMs,
		InputTokens:        int32(usage.PromptTokens),
		OutputTokens:       int32(usage.CompletionTokens),
		IsStream:           lc.isStream,
	}
	if usage.CacheReadTokens != nil {
		entry.CacheReadTokens = int32(*usage.CacheReadTokens)
	}
	_ = g.Storage.Logs().AppendBatch([]storage.RequestLog{entry})
}

func newRequestID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "req_" + hex.EncodeToString(buf[:])
}
