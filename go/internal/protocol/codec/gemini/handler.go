package gemini

import (
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// GenerateContentHandler is the Google Gemini codec.EndpointHandler for both
// generateContent and streamGenerateContent.
type GenerateContentHandler struct{}

func (GenerateContentHandler) Endpoint() ids.ProtocolEndpoint {
	return ids.GeminiGenerateContentV1Beta
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
