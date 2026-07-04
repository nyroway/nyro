package embeddings

import (
	"encoding/json"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

func TestRegistryHasEmbeddings(t *testing.T) {
	t.Parallel()
	h, ok := codec.Get(ids.OpenAICompatibleEmbeddingsV1)
	if !ok {
		t.Fatal("Embeddings handler not registered")
	}
	if h.Endpoint() != ids.OpenAICompatibleEmbeddingsV1 {
		t.Errorf("endpoint mismatch: %v", h.Endpoint())
	}
}

func TestRequestModelSwap(t *testing.T) {
	t.Parallel()
	in := `{"model":"alias","input":"hello","encoding_format":"float"}`
	req, err := requestDecoder{}.Decode([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "alias" {
		t.Errorf("model=%q", req.Model)
	}
	// Simulate the dispatcher resolving the client alias to the backend model.
	req.Model = "text-embedding-3-small"
	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if out.Path != "/v1/embeddings" {
		t.Errorf("path=%q", out.Path)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out.Body, &obj); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	var m, input string
	_ = json.Unmarshal(obj["model"], &m)
	_ = json.Unmarshal(obj["input"], &input)
	if m != "text-embedding-3-small" {
		t.Errorf("model swapped=%q", m)
	}
	if input != "hello" {
		t.Errorf("input lost=%q", input)
	}
}
