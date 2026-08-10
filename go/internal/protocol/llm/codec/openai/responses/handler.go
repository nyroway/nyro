package responses

import (
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

// ResponsesHandler is the OpenAI Responses /v1/responses codec.EndpointHandler.
type ResponsesHandler struct{}

func (ResponsesHandler) Endpoint() spec.ProtocolEndpoint { return spec.OpenAIResponsesV1 }

func (ResponsesHandler) Capabilities() spec.EndpointCapabilities {
	return spec.EndpointCapabilities{
		IngressRoutes: []spec.IngressRoute{{Method: "POST", Pattern: "/v1/responses"}},
		Streaming:     true,
	}
}

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
