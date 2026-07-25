package embeddings

import (
	"github.com/nyroway/nyro/go/llm/codec"
	"github.com/nyroway/nyro/go/llm/spec"
)

// EmbeddingsHandler is the OpenAI-compatible /v1/embeddings EndpointHandler.
type EmbeddingsHandler struct{}

func (EmbeddingsHandler) Endpoint() spec.ProtocolEndpoint { return spec.OpenAIEmbeddingsV1 }

// Capabilities reports Streaming: false — embeddings return a single JSON body,
// so the dispatcher copies the upstream response verbatim rather than routing
// it through the IR. That used to be an endpoint-ID comparison in the
// dispatcher; it is a property of the endpoint, so it lives here.
func (EmbeddingsHandler) Capabilities() spec.EndpointCapabilities {
	return spec.EndpointCapabilities{
		IngressRoutes: []spec.IngressRoute{{Method: "POST", Pattern: "/v1/embeddings"}},
		Streaming:     false,
	}
}

func (EmbeddingsHandler) MakeRequestDecoder() codec.RequestDecoder { return requestDecoder{} }
func (EmbeddingsHandler) MakeRequestEncoder() codec.RequestEncoder { return requestEncoder{} }

func (EmbeddingsHandler) MakeResponseDecoder() codec.ResponseDecoder { return responseDecoder{} }
func (EmbeddingsHandler) MakeResponseEncoder() codec.ResponseEncoder { return responseEncoder{} }

func (EmbeddingsHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (EmbeddingsHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(EmbeddingsHandler{})
}
