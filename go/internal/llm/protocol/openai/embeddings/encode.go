package embeddings

import (
	"encoding/json"
	"fmt"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type requestEncoder struct{}

// Encode writes the normalized embedding fields and preserves unknown ingress
// extensions without retaining the original wire envelope in the LLM model.
func (requestEncoder) Encode(req *llm.EmbeddingRequest) (protocol.WireRequest, error) {
	obj := make(map[string]json.RawMessage, len(req.Meta.Vendor.Ingress)+3)
	for key, value := range req.Meta.Vendor.Ingress {
		obj[key] = value
	}
	setJSON(obj, "model", req.Model)
	if req.Input != nil {
		input, err := encodeInput(req.Input)
		if err != nil {
			return protocol.WireRequest{}, err
		}
		obj["input"] = input
	}
	if req.Dimensions != nil {
		setJSON(obj, "dimensions", req.Dimensions)
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return protocol.WireRequest{}, err
	}
	return protocol.WireRequest{
		Method:  "POST",
		Path:    "/v1/embeddings",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Stream:  false,
	}, nil
}

func encodeInput(input llm.EmbeddingInput) (json.RawMessage, error) {
	var value any
	switch input := input.(type) {
	case *llm.TextInput:
		value = input.Text
	case *llm.TextBatchInput:
		value = input.Texts
	case *llm.TokenInput:
		value = input.Tokens
	case *llm.TokenBatchInput:
		value = input.Batches
	default:
		return nil, fmt.Errorf("unsupported embedding input type %T", input)
	}
	return json.Marshal(value)
}

func setJSON(obj map[string]json.RawMessage, key string, value any) {
	raw, _ := json.Marshal(value)
	obj[key] = raw
}
