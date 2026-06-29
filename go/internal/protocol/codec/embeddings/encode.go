package embeddings

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

type requestEncoder struct{}

// Encode rebuilds the upstream body from the preserved raw body with the model
// swapped to the resolved upstream model.
func (requestEncoder) Encode(req *ir.AiRequest) (codec.OutboundRequest, error) {
	raw := req.Meta.Vendor.Ingress[BodyKey]
	body := []byte(raw)
	if len(raw) == 0 {
		body = []byte(`{}`)
		raw = body
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		m, _ := json.Marshal(req.Model)
		obj["model"] = m
		body, _ = json.Marshal(obj)
	}
	return codec.OutboundRequest{
		Method:  "POST",
		Path:    "/v1/embeddings",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Stream:  false,
	}, nil
}
