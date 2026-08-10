package generatecontent

import (
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

// GenerateContentHandler is the Google Gemini codec.EndpointHandler for both
// generateContent and streamGenerateContent.
type GenerateContentHandler struct{}

func (GenerateContentHandler) Endpoint() spec.ProtocolEndpoint {
	return spec.GeminiGenerateContentV1Beta
}

// Capabilities claims Gemini's single ingress route. The {resource} parameter
// carries "{model}:{action}" in one path segment; the `.+:.+` constraint means
// a resource without a colon simply does not match, so the router answers 404
// instead of the codec having to reject it downstream.
func (GenerateContentHandler) Capabilities() spec.EndpointCapabilities {
	return spec.EndpointCapabilities{
		IngressRoutes: []spec.IngressRoute{
			{Method: "POST", Pattern: "/v1beta/models/{resource:.+:.+}"},
		},
		Streaming: true,
	}
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
