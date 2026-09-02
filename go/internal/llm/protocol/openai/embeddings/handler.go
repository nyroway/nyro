package embeddings

import (
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type ingress struct{}

func NewIngress() protocol.IngressCodec     { return ingress{} }
func (ingress) Endpoint() protocol.Endpoint { return protocol.OpenAIEmbeddingsV1 }
func (ingress) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{
		IngressRoutes:     []protocol.IngressRoute{{Method: "POST", Pattern: "/v1/embeddings"}},
		OpaquePassthrough: true,
		ErrorPassthrough:  true,
	}
}
func (ingress) IngressCodec() {}
func (ingress) DecodeRequest(request protocol.IngressRequest) (*llm.EmbeddingRequest, error) {
	return requestDecoder{}.Decode(request.Body)
}

type egress struct{}

func NewEgress() protocol.EgressCodec      { return egress{} }
func (egress) Endpoint() protocol.Endpoint { return protocol.OpenAIEmbeddingsV1 }
func (egress) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{OpaquePassthrough: true, ErrorPassthrough: true}
}
func (egress) EgressCodec() {}
func (egress) EncodeRequest(request *llm.EmbeddingRequest) (protocol.WireRequest, error) {
	return requestEncoder{}.Encode(request)
}

var _ protocol.EmbeddingIngressCodec = ingress{}
var _ protocol.EmbeddingEgressCodec = egress{}
