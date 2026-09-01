package embeddings

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

var _ codec.EmbeddingEndpointHandler = EmbeddingsHandler{}

func TestRegistryHasEmbeddings(t *testing.T) {
	t.Parallel()
	h, ok := codec.Get(spec.OpenAIEmbeddingsV1)
	if !ok {
		t.Fatal("Embeddings handler not registered")
	}
	if h.Endpoint() != spec.OpenAIEmbeddingsV1 {
		t.Errorf("endpoint mismatch: %v", h.Endpoint())
	}
}

func TestEmbeddingInputShapesRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantType any
	}{
		{name: "text", input: `"hello"`, wantType: &llm.TextInput{}},
		{name: "text batch", input: `["hello","world"]`, wantType: &llm.TextBatchInput{}},
		{name: "tokens", input: `[1,2,3]`, wantType: &llm.TokenInput{}},
		{name: "token batch", input: `[[1,2],[3,4]]`, wantType: &llm.TokenBatchInput{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := requestDecoder{}.Decode([]byte(`{"model":"alias","input":` + tt.input + `}`))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got, want := reflect.TypeOf(req.Input), reflect.TypeOf(tt.wantType); got != want {
				t.Fatalf("input type = %v, want %v", got, want)
			}
			out, err := requestEncoder{}.Encode(req)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(out.Body, &obj); err != nil {
				t.Fatalf("decode output: %v", err)
			}
			if got := string(obj["input"]); got != tt.input {
				t.Fatalf("input round-trip = %s, want %s", got, tt.input)
			}
		})
	}
}

func TestRequestModelSwap(t *testing.T) {
	t.Parallel()
	in := `{"model":"alias","input":["hello","world"],"encoding_format":"float","dimensions":128,"user":"consumer","vendor_flag":true}`
	req, err := requestDecoder{}.Decode([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "alias" {
		t.Errorf("model=%q", req.Model)
	}
	input, ok := req.Input.(*llm.TextBatchInput)
	if !ok {
		t.Fatalf("input type = %T, want *llm.TextBatchInput", req.Input)
	}
	if len(input.Texts) != 2 || input.Texts[0] != "hello" || input.Texts[1] != "world" {
		t.Fatalf("input texts = %#v", input.Texts)
	}
	if req.Dimensions == nil || *req.Dimensions != 128 {
		t.Fatalf("dimensions = %v, want 128", req.Dimensions)
	}
	// Simulate the dispatcher resolving the client alias to the backend model.
	req.SetModelID("text-embedding-3-small")
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
	var m string
	var texts []string
	var encodingFormat, user string
	var dimensions uint32
	var vendorFlag bool
	_ = json.Unmarshal(obj["model"], &m)
	_ = json.Unmarshal(obj["input"], &texts)
	_ = json.Unmarshal(obj["encoding_format"], &encodingFormat)
	_ = json.Unmarshal(obj["dimensions"], &dimensions)
	_ = json.Unmarshal(obj["user"], &user)
	_ = json.Unmarshal(obj["vendor_flag"], &vendorFlag)
	if m != "text-embedding-3-small" {
		t.Errorf("model swapped=%q", m)
	}
	if len(texts) != 2 || texts[0] != "hello" || texts[1] != "world" {
		t.Errorf("input lost=%#v", texts)
	}
	if encodingFormat != "float" || user != "consumer" {
		t.Errorf("protocol fields lost: encoding=%q user=%q", encodingFormat, user)
	}
	if dimensions != 128 {
		t.Errorf("dimensions = %d, want 128", dimensions)
	}
	if !vendorFlag {
		t.Error("vendor extension was not preserved")
	}
}
