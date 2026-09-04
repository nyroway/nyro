package generatecontent

import (
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type ingress struct{}

func NewIngress() protocol.IngressCodec     { return ingress{} }
func (ingress) Endpoint() protocol.Endpoint { return protocol.GeminiGenerateContentV1Beta }
func (ingress) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{
		IngressRoutes:    []protocol.IngressRoute{{Method: "POST", Pattern: "/v1beta/models/{resource:.+:.+}"}},
		Streaming:        true,
		ErrorPassthrough: true,
	}
}
func (ingress) IngressCodec() {}
func (ingress) DecodeRequest(request protocol.IngressRequest) (*llm.ChatRequest, error) {
	return requestDecoder{}.DecodeWithPath(request.Body, request.Params)
}

func (ingress) EncodeResponse(response *llm.ChatResponse) (protocol.WireResponse, error) {
	body, err := responseEncoder{}.Format(response)
	return protocol.WireResponse{Status: 200, Body: body}, err
}
func (ingress) NewStreamEncoder() protocol.StreamEncoder { return &streamResponseEncoder{} }

type egress struct{}

func NewEgress() protocol.EgressCodec      { return egress{} }
func (egress) Endpoint() protocol.Endpoint { return protocol.GeminiGenerateContentV1Beta }
func (egress) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{Streaming: true, ErrorPassthrough: true}
}
func (egress) EgressCodec() {}
func (egress) EncodeRequest(request *llm.ChatRequest) (protocol.WireRequest, error) {
	return requestEncoder{}.Encode(request)
}

func (egress) DecodeResponse(response protocol.WireResponse) (*llm.ChatResponse, error) {
	return responseDecoder{}.Parse(response.Body)
}
func (egress) NewStreamDecoder() protocol.StreamDecoder { return &streamResponseDecoder{} }

var (
	_ protocol.ChatIngressCodec = ingress{}
	_ protocol.ChatEgressCodec  = egress{}
)
