package embeddings

import (
	"encoding/json"
	"errors"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

type requestDecoder struct{}

// Decode extracts the model (for routing) and preserves the raw body verbatim
// in the vendor ingress bag.
func (requestDecoder) Decode(body []byte) (*ir.AiRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	modelRaw, ok := obj["model"]
	if !ok {
		return nil, errors.New("model is required for embeddings")
	}
	var model string
	if json.Unmarshal(modelRaw, &model) != nil || model == "" {
		return nil, errors.New("model is required for embeddings")
	}
	req := ir.NewAiRequest(model, nil)
	req.Meta.SourceProtocol = &ids.OpenAIEmbeddingsV1
	req.Meta.Vendor.Ingress = map[string]json.RawMessage{BodyKey: body}
	return req, nil
}
