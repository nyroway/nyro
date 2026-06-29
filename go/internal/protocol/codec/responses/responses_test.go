package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

func TestRegistryHasResponses(t *testing.T) {
	t.Parallel()
	h, ok := codec.Get(ids.OpenAIResponsesV1)
	if !ok {
		t.Fatal("Responses handler not registered")
	}
	if h.Endpoint() != ids.OpenAIResponsesV1 {
		t.Errorf("endpoint mismatch: %v", h.Endpoint())
	}
}

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	in := `{"model":"gpt-4o","instructions":"be brief",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"stream":true,"temperature":0.5}`

	req, err := requestDecoder{}.Decode([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// instructions → system message
	if len(req.Messages) != 2 || req.Messages[0].Role != ir.RoleSystem || ir.ToText(req.Messages[0].Content) != "be brief" {
		t.Errorf("system message wrong: %+v", req.Messages)
	}
	if ir.ToText(req.Messages[1].Content) != "hi" {
		t.Errorf("user text wrong: %+v", req.Messages[1])
	}
	if !req.Stream.Enabled {
		t.Error("stream should be enabled")
	}

	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if out.Path != "/v1/responses" {
		t.Errorf("path=%q", out.Path)
	}
	var w request
	if err := json.Unmarshal(out.Body, &w); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if w.Model != "gpt-4o" || w.Instructions != "be brief" || !w.Stream {
		t.Errorf("wire wrong: %+v", w)
	}
}

func TestNonStreamResponse(t *testing.T) {
	t.Parallel()
	body := `{"id":"r1","object":"response","model":"gpt-4o",` +
		`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],` +
		`"status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`

	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello" || resp.Usage.TotalTokens != 5 {
		t.Errorf("resp=%+v", resp)
	}

	out, err := responseEncoder{}.Format(resp)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var w response
	if err := json.Unmarshal(out, &w); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if w.Object != "response" || len(w.Output) != 1 {
		t.Errorf("wire=%+v", w)
	}
}

func TestStreamDecode(t *testing.T) {
	t.Parallel()
	d := &streamResponseDecoder{}
	events := []string{
		`{"type":"response.created","response":{"id":"r1","model":"gpt-4o","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"Hi"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
	}
	var text strings.Builder
	var sawStart, sawDone bool
	var total uint32
	for _, e := range events {
		deltas, _ := d.ParseChunk(e)
		for _, dl := range deltas {
			switch v := dl.(type) {
			case *ir.MessageStartDelta:
				sawStart = true
			case *ir.TextDelta:
				text.WriteString(v.Text)
			case *ir.UsageDelta:
				total = v.Usage.TotalTokens
			case *ir.DoneDelta:
				sawDone = true
			}
		}
	}
	if !sawStart || text.String() != "Hi" || total != 3 || !sawDone {
		t.Errorf("decode: start=%v text=%q total=%d done=%v", sawStart, text.String(), total, sawDone)
	}
}

func TestStreamEncode(t *testing.T) {
	t.Parallel()
	e := &streamResponseEncoder{}
	deltas := []ir.StreamDelta{
		&ir.MessageStartDelta{ID: "r1", Model: "gpt-4o"},
		&ir.TextDelta{Text: "Hi"},
		&ir.UsageDelta{Usage: ir.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
		&ir.DoneDelta{StopReason: "completed"},
	}
	frames, err := e.FormatDeltas(deltas)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var joined strings.Builder
	for _, f := range frames {
		joined.Write(f.Bytes())
	}
	out := joined.String()
	for _, want := range []string{
		`"type":"response.created"`,
		`"type":"response.output_item.added"`,
		`"type":"response.content_part.added"`,
		`"type":"response.output_text.delta"`,
		`"delta":"Hi"`,
		`"type":"response.output_text.done"`,
		`"type":"response.content_part.done"`,
		`"type":"response.output_item.done"`,
		`"type":"response.completed"`,
		`"total_tokens":3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
