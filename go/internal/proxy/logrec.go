package proxy

import "net/http"

// statusRecorder wraps http.ResponseWriter to capture the response status for
// telemetry (the OnLog phase hook records it as nyro.client_status). It
// forwards Flush so SSE streaming works through it.
//
// The per-request audit row is no longer written here: the OnLog phase hook
// (registered once at startup in cmd/gateway) emits the structured LogRecord
// via OTel. The dispatcher populates the per-request ContextBag and the hook
// reads it. See internal/observability/hooks.go.
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
