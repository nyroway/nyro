package proxy

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/codec/anthropic"  // register Anthropic codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/embeddings" // register Embeddings codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/gemini"     // register Gemini codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/openai"
	"github.com/nyroway/nyro/go/internal/protocol/codec/responses" // register Responses codec
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/webutil"
)

// NewRouter builds the chi router with the proxy routes wired. Referencing the
// codec packages forces their init() to run, registering each EndpointHandler.
func NewRouter(gw *Gateway) chi.Router {
	_ = openai.ChatCompletionsHandler{} // ensure openai init() ran
	_ = anthropic.MessagesHandler{}     // ensure anthropic init() ran
	_ = gemini.GenerateContentHandler{} // ensure gemini init() ran
	_ = responses.ResponsesHandler{}    // ensure responses init() ran
	_ = embeddings.EmbeddingsHandler{}  // ensure embeddings init() ran

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		webutil.JSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	// GET /readyz — readiness probe gated on config-cache fill. The gateway no
	// longer holds a storage handle (P3c), so readiness is "has a config
	// snapshot been published (config-sync push / YAML build)?" — nil means not ready.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if gw.Cache.Load() == nil {
			webutil.JSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unready"})
			return
		}
		webutil.JSON(w, http.StatusOK, map[string]any{"status": "ready"})
	})

	// GET /v1/models — OpenAI-compatible client discovery (API-key-aware).
	r.Get("/v1/models", func(w http.ResponseWriter, r *http.Request) { handleModelsList(w, r, gw) })

	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleProxy(w, r, gw, ids.OpenAIChatCompletionsV1, "", false)
	})
	r.Post("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handleProxy(w, r, gw, ids.AnthropicMessages20230601, "", false)
	})
	r.Post("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		handleProxy(w, r, gw, ids.OpenAIResponsesV1, "", false)
	})
	r.Post("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handleProxy(w, r, gw, ids.OpenAIEmbeddingsV1, "", false)
	})
	// Gemini embeds the model + action in the path: /v1beta/models/{model}:{action}
	r.Post("/v1beta/models/{resource}", func(w http.ResponseWriter, r *http.Request) {
		model, action, ok := strings.Cut(chi.URLParam(r, "resource"), ":")
		if !ok {
			webutil.Error(w, http.StatusNotFound, "malformed Gemini path, expected models/{model}:{action}", "GATEWAY_ERROR")
			return
		}
		handleProxy(w, r, gw, ids.GeminiGenerateContentV1Beta, model, action == "streamGenerateContent")
	})
	return r
}

// handleProxy is the ingress shell: it resolves the codec, decodes the wire
// body into IR (using the path model for Gemini), then hands off to Dispatch.
func handleProxy(w http.ResponseWriter, r *http.Request, gw *Gateway, ep ids.ProtocolEndpoint, pathModel string, pathStream bool) {
	h, ok := codec.Get(ep)
	if !ok {
		webutil.Error(w, http.StatusNotImplemented, "no codec registered for endpoint", "GATEWAY_ERROR")
		return
	}
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

	var req *ir.AiRequest
	if pathModel != "" {
		md, ok := h.MakeRequestDecoder().(codec.PathModelDecoder)
		if !ok {
			webutil.Error(w, http.StatusInternalServerError, "codec does not support path-model decode", "GATEWAY_ERROR")
			return
		}
		req, err = md.DecodeWithModel(body, pathModel, pathStream)
	} else {
		req, err = h.MakeRequestDecoder().Decode(body)
	}
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, "decode request: "+err.Error(), "GATEWAY_ERROR")
		return
	}

	gw.Dispatch(w, r, req, h)
}
