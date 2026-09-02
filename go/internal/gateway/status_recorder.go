package gateway

import (
	"net/http"
)

// statusRecorder wraps http.ResponseWriter to capture the response status for
// telemetry (the Observe Phase records it as http.response.status_code). It
// forwards Flush so SSE streaming works through it.
//
// Runtime owns the canonical status recorded by telemetry; this wrapper keeps
// the transitional HTTP adapter's observable status for handler tests.
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
