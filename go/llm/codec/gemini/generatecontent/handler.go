package generatecontent

import (
	"github.com/nyroway/nyro/go/llm/codec"
	"github.com/nyroway/nyro/go/llm/spec"
)

// GenerateContentHandler is the Google Gemini codec.EndpointHandler for both
// generateContent and streamGenerateContent.
type GenerateContentHandler struct{}

func (GenerateContentHandler) Endpoint() spec.ProtocolEndpoint {
	return spec.GeminiGenerateContentV1Beta
}

func (GenerateContentHandler) MakeRequestDecoder() codec.RequestDecoder { return requestDecoder{} }

func (GenerateContentHandler) MakeRequestEncoder() codec.RequestEncoder { return requestEncoder{} }

func (GenerateContentHandler) MakeResponseDecoder() codec.ResponseDecoder { return responseDecoder{} }

func (GenerateContentHandler) MakeResponseEncoder() codec.ResponseEncoder { return responseEncoder{} }

func (GenerateContentHandler) MakeStreamResponseDecoder() codec.StreamResponseDecoder {
	return &streamResponseDecoder{}
}

func (GenerateContentHandler) MakeStreamResponseEncoder() codec.StreamResponseEncoder {
	return &streamResponseEncoder{}
}

func init() {
	codec.Register(GenerateContentHandler{})
}
