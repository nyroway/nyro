package messages

import (
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

// MessagesHandler is the Anthropic /v1/messages codec.EndpointHandler.
type MessagesHandler struct{}

func (MessagesHandler) Endpoint() spec.ProtocolEndpoint { return spec.AnthropicMessagesV1 }

func (MessagesHandler) Capabilities() spec.EndpointCapabilities {
	return spec.EndpointCapabilities{
		IngressRoutes: []spec.IngressRoute{{Method: "POST", Pattern: "/v1/messages"}},
		Streaming:     true,
	}
}

func (MessagesHandler) MakeRequestDecoder() codec.ChatRequestDecoder   { return requestDecoder{} }
func (MessagesHandler) MakeRequestEncoder() codec.ChatRequestEncoder   { return requestEncoder{} }
func (MessagesHandler) MakeResponseDecoder() codec.ChatResponseDecoder { return responseDecoder{} }
func (MessagesHandler) MakeResponseEncoder() codec.ChatResponseEncoder { return responseEncoder{} }

func (MessagesHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (MessagesHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(MessagesHandler{})
}
