package messages

import (
	"github.com/nyroway/nyro/go/llm/codec"
	"github.com/nyroway/nyro/go/llm/spec"
)

// MessagesHandler is the Anthropic /v1/messages codec.EndpointHandler.
type MessagesHandler struct{}

func (MessagesHandler) Endpoint() spec.ProtocolEndpoint { return spec.AnthropicMessages20230601 }

func (MessagesHandler) MakeRequestDecoder() codec.RequestDecoder   { return requestDecoder{} }
func (MessagesHandler) MakeRequestEncoder() codec.RequestEncoder   { return requestEncoder{} }
func (MessagesHandler) MakeResponseDecoder() codec.ResponseDecoder { return responseDecoder{} }
func (MessagesHandler) MakeResponseEncoder() codec.ResponseEncoder { return responseEncoder{} }

func (MessagesHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (MessagesHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(MessagesHandler{})
}
