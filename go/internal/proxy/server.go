package proxy

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/codec/anthropic"  // register Anthropic codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/embeddings" // register Embeddings codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/gemini"     // register Gemini codec
	"github.com/nyroway/nyro/go/internal/protocol/codec/openai"
	"github.com/nyroway/nyro/go/internal/protocol/codec/responses" // register Responses codec
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// NewRouter builds the Gin engine with the proxy routes wired. Referencing the
// codec packages forces their init() to run, registering each EndpointHandler.
func NewRouter(gw *Gateway) *gin.Engine {
	_ = openai.ChatCompletionsHandler{} // ensure openai init() ran
	_ = anthropic.MessagesHandler{}     // ensure anthropic init() ran
	_ = gemini.GenerateContentHandler{} // ensure gemini init() ran
	_ = responses.ResponsesHandler{}    // ensure responses init() ran
	_ = embeddings.EmbeddingsHandler{}  // ensure embeddings init() ran

	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	// GET /readyz — readiness probe gated on storage health (CanConnect + Writable).
	r.GET("/readyz", func(c *gin.Context) {
		h, err := gw.Storage.Bootstrap().Health()
		if err != nil || !h.CanConnect || !h.Writable {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unready", "backend": h.Backend})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready", "backend": h.Backend})
	})

	// GET /v1/models — OpenAI-compatible client discovery (API-key-aware).
	r.GET("/v1/models", func(c *gin.Context) { handleModelsList(c, gw) })

	r.POST("/v1/chat/completions", func(c *gin.Context) {
		handleProxy(c, gw, ids.OpenAICompatibleChatCompletionsV1, "", false)
	})
	r.POST("/v1/messages", func(c *gin.Context) {
		handleProxy(c, gw, ids.AnthropicMessages20230601, "", false)
	})
	r.POST("/v1/responses", func(c *gin.Context) {
		handleProxy(c, gw, ids.OpenAIResponsesV1, "", false)
	})
	r.POST("/v1/embeddings", func(c *gin.Context) {
		handleProxy(c, gw, ids.OpenAICompatibleEmbeddingsV1, "", false)
	})
	// Gemini embeds the model + action in the path: /v1beta/models/{model}:{action}
	r.POST("/v1beta/models/:resource", func(c *gin.Context) {
		model, action, ok := strings.Cut(c.Param("resource"), ":")
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"message": "malformed Gemini path, expected models/{model}:{action}", "type": "gateway_error",
			}})
			return
		}
		handleProxy(c, gw, ids.GoogleGeminiGenerateContentV1Beta, model, action == "streamGenerateContent")
	})
	return r
}

// handleProxy is the ingress shell: it resolves the codec, decodes the wire
// body into IR (using the path model for Gemini), then hands off to Dispatch.
func handleProxy(c *gin.Context, gw *Gateway, ep ids.ProtocolEndpoint, pathModel string, pathStream bool) {
	h, ok := codec.Get(ep)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{
			"message": "no codec registered for endpoint", "type": "gateway_error",
		}})
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "read body: " + err.Error(), "type": "gateway_error",
		}})
		return
	}

	var req *ir.AiRequest
	if pathModel != "" {
		md, ok := h.MakeRequestDecoder().(codec.PathModelDecoder)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"message": "codec does not support path-model decode", "type": "gateway_error",
			}})
			return
		}
		req, err = md.DecodeWithModel(body, pathModel, pathStream)
	} else {
		req, err = h.MakeRequestDecoder().Decode(body)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "decode request: " + err.Error(), "type": "gateway_error",
		}})
		return
	}

	gw.Dispatch(c.Writer, c.Request, req, h)
}
