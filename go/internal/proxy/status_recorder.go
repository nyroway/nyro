package proxy

import (
	"net/http"

	"github.com/nyroway/nyro/go/internal/pipeline"
)

// statusRecorder wraps http.ResponseWriter to capture the response status for
// telemetry (the observability Stage records it as http.response.status_code). It
// forwards Flush so SSE streaming works through it.
//
// It writes the status straight onto the Exchange rather than letting the
// dispatcher copy it afterwards: the telemetry Stage emits from a defer that
// runs while the chain is still unwinding, so a status assigned after
// Chain.Run returns would be too late to be reported.
//
// The per-request audit row is not written here — the observability Stage
// emits the structured LogRecord via OTel from the state on the Exchange. See
// internal/telemetry/stage.go.
type statusRecorder struct {
	http.ResponseWriter
	ex          *pipeline.Exchange
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
		if r.ex != nil {
			r.ex.Status = code
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
