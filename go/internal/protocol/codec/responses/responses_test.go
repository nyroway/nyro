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

// TestNonStreamStopReason guards the Responses status → canonical stop-reason
// mapping: raw "completed"/"incomplete" are not valid downstream vocabulary,
// and a completed response carrying a function_call must become tool_calls so
// clients run the tool instead of ending the turn.
func TestNonStreamStopReason(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, body, want string }{
		{"completed_text", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`, "stop"},
		{"incomplete", `{"status":"incomplete","output":[]}`, "length"},
		{"completed_tool", `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"get_weather","arguments":"{}"}]}`, "tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := responseDecoder{}.Parse([]byte(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if resp.StopReason != tc.want {
				t.Errorf("StopReason=%q, want %q", resp.StopReason, tc.want)
			}
		})
	}
}

// TestStreamStopReason is the streaming counterpart: a function_call item yields
// tool_calls, and a response.incomplete event becomes length — never the raw
// event-type string.
func TestStreamStopReason(t *testing.T) {
	t.Parallel()
	stopOf := func(events []string) string {
		d := &streamResponseDecoder{}
		var stop string
		for _, e := range events {
			deltas, _ := d.ParseChunk(e)
			for _, dl := range deltas {
				if v, ok := dl.(*ir.DoneDelta); ok {
					stop = v.StopReason
				}
			}
		}
		return stop
	}
	if got := stopOf([]string{
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"c1","name":"get_weather"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}); got != "tool_calls" {
		t.Errorf("tool stream stop=%q, want tool_calls", got)
	}
	if got := stopOf([]string{`{"type":"response.incomplete"}`}); got != "length" {
		t.Errorf("incomplete stream stop=%q, want length", got)
	}
}

// TestReasoningRoundTrip guards that a reasoning output item is decoded into
// IR reasoning (not dropped) and re-encoded back as a reasoning item.
func TestReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	body := `{"status":"completed","output":[` +
		`{"type":"reasoning","content":[{"type":"reasoning_text","text":"17*23=391"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"391"}]}]}`
	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.ReasoningContent != "17*23=391" {
		t.Errorf("ReasoningContent=%q, want the reasoning text", resp.ReasoningContent)
	}
	out, _ := responseEncoder{}.Format(resp)
	if !strings.Contains(string(out), `"type":"reasoning"`) || !strings.Contains(string(out), "17*23=391") {
		t.Errorf("encoded output missing reasoning item:\n%s", out)
	}
}

// TestImageRequestEncoding guards that an image block survives request encoding
// as an input_image part — ir.ToText alone dropped it.
func TestImageRequestEncoding(t *testing.T) {
	t.Parallel()
	req := &ir.AiRequest{
		Model: "m",
		Messages: []ir.Message{{
			Role: ir.RoleUser,
			Content: &ir.BlocksContent{Blocks: []ir.ContentBlock{
				&ir.TextBlock{Text: "what is this"},
				&ir.ImageBlock{Source: &ir.Base64Media{MediaType: "image/png", Data: "AAAA"}},
			}},
		}},
	}
	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(out.Body), `"input_image"`) || !strings.Contains(string(out.Body), "data:image/png;base64,AAAA") {
		t.Errorf("outbound request missing input_image:\n%s", out.Body)
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
