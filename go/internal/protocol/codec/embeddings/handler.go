package embeddings

import (
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// EmbeddingsHandler is the OpenAI-compatible /v1/embeddings EndpointHandler.
type EmbeddingsHandler struct{}

func (EmbeddingsHandler) Endpoint() ids.ProtocolEndpoint { return ids.OpenAICompatibleEmbeddingsV1 }

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
