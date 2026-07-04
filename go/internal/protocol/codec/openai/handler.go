package openai

import (
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// ChatCompletionsHandler is the OpenAI-compatible /v1/chat/completions
// codec.EndpointHandler. Registering it in init() makes it discoverable via
// codec.Get(ids.OpenAICompatibleChatCompletionsV1).
type ChatCompletionsHandler struct{}

func (ChatCompletionsHandler) Endpoint() ids.ProtocolEndpoint {
	return ids.OpenAICompatibleChatCompletionsV1
}

func (ChatCompletionsHandler) MakeRequestDecoder() codec.RequestDecoder   { return requestDecoder{} }
func (ChatCompletionsHandler) MakeRequestEncoder() codec.RequestEncoder   { return requestEncoder{} }
func (ChatCompletionsHandler) MakeResponseDecoder() codec.ResponseDecoder { return responseDecoder{} }
func (ChatCompletionsHandler) MakeResponseEncoder() codec.ResponseEncoder { return responseEncoder{} }

func (ChatCompletionsHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (ChatCompletionsHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(ChatCompletionsHandler{})
}
