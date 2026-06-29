package responses

import (
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// ResponsesHandler is the OpenAI Responses /v1/responses codec.EndpointHandler.
type ResponsesHandler struct{}

func (ResponsesHandler) Endpoint() ids.ProtocolEndpoint { return ids.OpenAIResponsesV1 }

func (ResponsesHandler) MakeRequestDecoder() codec.RequestDecoder   { return requestDecoder{} }
func (ResponsesHandler) MakeRequestEncoder() codec.RequestEncoder   { return requestEncoder{} }
func (ResponsesHandler) MakeResponseDecoder() codec.ResponseDecoder { return responseDecoder{} }
func (ResponsesHandler) MakeResponseEncoder() codec.ResponseEncoder { return responseEncoder{} }

func (ResponsesHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (ResponsesHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(ResponsesHandler{})
}
