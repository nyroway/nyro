package gateway

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/webutil"
)

// NewRouter builds the chi router with the Gateway routes wired.
func NewRouter(gw *Gateway) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		webutil.JSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	// GET /readyz — readiness requires both a published config snapshot and
	// healthy quota State.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !gw.Ready() {
			webutil.JSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unready"})
			return
		}
		webutil.JSON(w, http.StatusOK, map[string]any{"status": "ready"})
	})

	// GET /v1/models — OpenAI-compatible client discovery (API-key-aware).
	r.Get("/v1/models", func(w http.ResponseWriter, r *http.Request) { handleModelsList(w, r, gw) })

	// Ingress routes are declared by the codecs themselves, so this loop is the
	// whole route table: every explicitly composed codec gets the (method, pattern)
	// pairs from its Capabilities().
	if gw.Protocols != nil {
		for _, h := range gw.Protocols.IngressEndpoints() {
			h := h
			for _, route := range h.Capabilities().IngressRoutes {
				r.MethodFunc(route.Method, route.Pattern, func(w http.ResponseWriter, r *http.Request) {
					handleProxy(w, r, gw, h)
				})
			}
		}
	}
	return r
}

// routeParams collects the parameters chi matched for the current route, so a
// ChatPathDecoder can read the parts of the URL its protocol puts there.
func routeParams(r *http.Request) map[string]string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return nil
	}
	params := make(map[string]string, len(rctx.URLParams.Keys))
	for i, k := range rctx.URLParams.Keys {
		if i < len(rctx.URLParams.Values) {
			params[k] = rctx.URLParams.Values[i]
		}
	}
	return params
}

// handleProxy is the ingress shell: it decodes the wire body into IR (letting
// the codec read the URL when its protocol puts the model there), then hands
// off to Dispatch.
func handleProxy(w http.ResponseWriter, r *http.Request, gw *Gateway, h protocol.IngressCodec) {
	limit := resolveProxySettings(gw.snapshot()).MaxBodyBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webutil.Error(w, http.StatusRequestEntityTooLarge, "request body too large", "GATEWAY_ERROR")
			return
		}
		webutil.Error(w, http.StatusBadRequest, "read body: "+err.Error(), "GATEWAY_ERROR")
		return
	}

	switch h := h.(type) {
	case protocol.ChatIngressCodec:
		var req *llm.ChatRequest
		req, err = h.DecodeRequest(protocol.IngressRequest{Body: body, Params: routeParams(r)})
		if err == nil {
			gw.Dispatch(w, r, req, h)
			return
		}
	case protocol.EmbeddingIngressCodec:
		var req *llm.EmbeddingRequest
		req, err = h.DecodeRequest(protocol.IngressRequest{Body: body, Params: routeParams(r)})
		if err == nil {
			gw.Dispatch(w, r, req, h)
			return
		}
	default:
		webutil.Error(w, http.StatusInternalServerError, "unsupported endpoint workload", "GATEWAY_ERROR")
		return
	}
	webutil.Error(w, http.StatusBadRequest, "decode request: "+err.Error(), "GATEWAY_ERROR")
}
