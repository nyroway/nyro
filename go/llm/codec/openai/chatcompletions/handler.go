package chatcompletions

import (
	"github.com/nyroway/nyro/go/llm/codec"
	"github.com/nyroway/nyro/go/llm/spec"
)

// ChatCompletionsHandler is the OpenAI-compatible /v1/chat/completions
// codec.EndpointHandler. Registering it in init() makes it discoverable via
// codec.Get(spec.OpenAIChatCompletionsV1).
type ChatCompletionsHandler struct{}

func (ChatCompletionsHandler) Endpoint() spec.ProtocolEndpoint {
	return spec.OpenAIChatCompletionsV1
}

func (ChatCompletionsHandler) Capabilities() spec.EndpointCapabilities {
	return spec.EndpointCapabilities{
		IngressRoutes: []spec.IngressRoute{{Method: "POST", Pattern: "/v1/chat/completions"}},
		Streaming:     true,
	}
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
