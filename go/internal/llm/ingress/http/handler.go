// Package httpingress adapts explicitly composed LLM protocol codecs to HTTP.
// It owns LLM northbound wire concerns and depends on transport-neutral LLM
// Runtime and Protocol contracts.
package httpingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// RuntimeSource leases the one immutable LLM Runtime used for a complete HTTP
// request. Production supplies a Kernel generation-backed implementation.
type RuntimeSource interface {
	Acquire() (runtime *llmruntime.Runtime, release func(), ok bool)
}

// Options reserves constructor configuration for HTTP-ingress policy owned by
// this package. Runtime-derived policy is queried from the acquired Runtime.
type Options struct{}

type handler struct {
	source RuntimeSource
}

// New constructs an LLM HTTP handler from only the explicitly supplied
// ingress codecs. No package registry or import side effect is consulted.
func New(catalog *protocol.Catalog, source RuntimeSource, _ Options) (http.Handler, error) {
	if catalog == nil {
		return nil, errors.New("LLM HTTP ingress: Protocol Catalog is required")
	}
	if source == nil {
		return nil, errors.New("LLM HTTP ingress: RuntimeSource is required")
	}
	routes, err := catalogRoutes(catalog)
	if err != nil {
		return nil, err
	}
	ingress := &handler{source: source}
	router := chi.NewRouter()
	router.Get("/v1/models", ingress.serveModels)
	for _, route := range routes {
		route := route
		router.MethodFunc(route.method, route.pattern, func(writer http.ResponseWriter, request *http.Request) {
			ingress.serveCodec(writer, request, route.codec)
		})
	}
	return router, nil
}

func (handler *handler) serveCodec(writer http.ResponseWriter, request *http.Request, codec protocol.IngressCodec) {
	runtime, release, ok := handler.source.Acquire()
	if release != nil {
		defer release()
	}
	if !ok || runtime == nil {
		writeCodecError(writer, codec, llm.NewError(llm.ErrServiceUnavailable, "LLM runtime is unavailable").WithStatus(http.StatusServiceUnavailable))
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, runtime.MaxBodyBytes())
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCodecError(writer, codec, llm.NewError(llm.ErrInvalidRequest, "request body too large").WithStatus(http.StatusRequestEntityTooLarge))
			return
		}
		writeCodecError(writer, codec, llm.NewError(llm.ErrInvalidRequest, "read body: "+err.Error()).WithStatus(http.StatusBadRequest))
		return
	}

	modelRequest, err := decodeRequest(codec, protocol.IngressRequest{Body: body, Params: routeParams(request)})
	if err != nil {
		writeCodecError(writer, codec, llm.NewError(llm.ErrInvalidRequest, "decode request: "+err.Error()).WithStatus(http.StatusBadRequest))
		return
	}

	sink := newSink(writer, codec)
	runtime.Execute(request.Context(), llmruntime.Call{
		Request:       modelRequest,
		Source:        codec.Endpoint(),
		Operation:     request.Method,
		Resource:      requestResource(request),
		Credentials:   credentialsFromRequest(request),
		ClientAddress: request.RemoteAddr,
		RequestID:     request.Header.Get("X-Request-ID"),
		Sink:          sink,
	})
}

func requestResource(request *http.Request) string {
	if request == nil || request.URL == nil {
		return ""
	}
	if escaped := request.URL.EscapedPath(); escaped != "" {
		return escaped
	}
	return request.URL.Path
}

func decodeRequest(codec protocol.IngressCodec, request protocol.IngressRequest) (llm.ModelRequest, error) {
	switch codec := codec.(type) {
	case protocol.ChatIngressCodec:
		return codec.DecodeRequest(request)
	case protocol.EmbeddingIngressCodec:
		return codec.DecodeRequest(request)
	default:
		return nil, fmt.Errorf("ingress endpoint %s has an unsupported workload", codec.Endpoint())
	}
}

func writeCodecError(writer http.ResponseWriter, codec protocol.IngressCodec, providerError *llm.Error) {
	wire, err := codec.EncodeError(providerError)
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if wire.Status == 0 {
		wire.Status = errorStatus(providerError)
	}
	sink := newSink(writer, codec)
	sink.terminated = true
	// A downstream write failure cannot be repaired by appending a second HTTP
	// response. Runtime observes Sink delivery failures on accepted calls; these
	// pre-execution errors therefore stop after the single attempted wire value.
	_ = sink.writeWire(context.Background(), wire, "application/json")
}

func credentialsFromRequest(request *http.Request) authn.Credentials {
	return authn.Credentials{APIKey: extractKey(request)}
}
