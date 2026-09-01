package embeddings

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nyroway/nyro/go/internal/llm"
)

type requestDecoder struct{}

// Decode normalizes an OpenAI-compatible embedding request while preserving
// unknown request fields in the ingress extension namespace.
func (requestDecoder) Decode(body []byte) (*llm.EmbeddingRequest, error) {
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

	input, err := decodeInput(obj["input"])
	if err != nil {
		return nil, err
	}
	req := llm.NewEmbeddingRequest(model, input)
	if raw := obj["dimensions"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &req.Dimensions); err != nil {
			return nil, fmt.Errorf("invalid embeddings dimensions: %w", err)
		}
	}

	for _, key := range []string{"model", "input", "dimensions"} {
		delete(obj, key)
	}
	if len(obj) > 0 {
		req.Meta.Vendor.Ingress = obj
	}
	return req, nil
}

func decodeInput(raw json.RawMessage) (llm.EmbeddingInput, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return &llm.TextInput{Text: text}, nil
	}
	var texts []string
	if json.Unmarshal(raw, &texts) == nil {
		return &llm.TextBatchInput{Texts: texts}, nil
	}
	var tokens []int
	if json.Unmarshal(raw, &tokens) == nil {
		return &llm.TokenInput{Tokens: tokens}, nil
	}
	var batches [][]int
	if json.Unmarshal(raw, &batches) == nil {
		return &llm.TokenBatchInput{Batches: batches}, nil
	}
	return nil, fmt.Errorf("unsupported embeddings input")
}
