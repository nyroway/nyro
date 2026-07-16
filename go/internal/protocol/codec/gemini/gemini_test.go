package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

func TestRegistryHasGemini(t *testing.T) {
	t.Parallel()
	h, ok := codec.Get(ids.GeminiGenerateContentV1Beta)
	if !ok {
		t.Fatal("Gemini handler not registered")
	}
	if h.Endpoint() != ids.GeminiGenerateContentV1Beta {
		t.Errorf("endpoint mismatch: %v", h.Endpoint())
	}
}

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],` +
		`"systemInstruction":{"parts":[{"text":"be brief"}]},` +
		`"generationConfig":{"temperature":0.5,"maxOutputTokens":100}}`

	req, err := requestDecoder{}.DecodeWithModel([]byte(in), "gemini-2.0-flash", true)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "gemini-2.0-flash" {
		t.Errorf("model=%q", req.Model)
	}
	if req.System != "be brief" {
		t.Errorf("system=%q", req.System)
	}
	if len(req.Messages) != 1 || ir.ToText(req.Messages[0].Content) != "hi" {
		t.Errorf("messages=%+v", req.Messages)
	}
	if !req.Stream.Enabled {
		t.Error("stream should be enabled")
	}
	if req.Generation.Temperature == nil || *req.Generation.Temperature != 0.5 {
		t.Errorf("temperature=%v", req.Generation.Temperature)
	}

	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(out.Path, "streamGenerateContent?alt=sse") {
		t.Errorf("stream path=%q", out.Path)
	}
	if !strings.Contains(out.Path, "gemini-2.0-flash") {
		t.Errorf("model not in path=%q", out.Path)
	}
	var w request
	if err := json.Unmarshal(out.Body, &w); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if len(w.Contents) != 1 || len(w.Contents[0].Parts) != 1 || w.Contents[0].Parts[0].Text != "hi" {
		t.Errorf("wire contents wrong: %+v", w.Contents)
	}
}

func TestNonStreamResponse(t *testing.T) {
	t.Parallel()
	body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},` +
		`"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`

	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello" || resp.StopReason != "stop" || resp.Usage.TotalTokens != 5 {
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
	if len(w.Candidates) != 1 || len(w.Candidates[0].Content.Parts) != 1 {
		t.Errorf("wire=%+v", w)
	}
}

// TestNonStreamToolCallStopReason guards the fix for Gemini function calls:
// Gemini returns finishReason=STOP even when emitting a functionCall, so the
// decoder must upgrade the canonical stop reason to "tool_calls" — otherwise
// downstream protocols emit end_turn and clients never run the tool.
func TestNonStreamToolCallStopReason(t *testing.T) {
	t.Parallel()
	body := `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},` +
		`"finishReason":"STOP"}]}`
	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("StopReason=%q, want tool_calls (functionCall present)", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("tool calls=%+v", resp.ToolCalls)
	}
}

// TestStreamToolCallStopReason is the streaming counterpart: a functionCall
// chunk (finishReason=STOP) must terminate the stream with a tool_calls stop
// reason.
func TestStreamToolCallStopReason(t *testing.T) {
	t.Parallel()
	d := &streamResponseDecoder{}
	chunk := `{"candidates":[{"content":{"parts":[` +
		`{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},` +
		`"finishReason":"STOP"}]}`
	deltas, err := d.ParseChunk(chunk)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var stop string
	for _, dl := range deltas {
		if v, ok := dl.(*ir.DoneDelta); ok {
			stop = v.StopReason
		}
	}
	if stop != "tool_calls" {
		t.Errorf("stream stop=%q, want tool_calls", stop)
	}
}

// TestResponseEncoderFinishReason guards the reverse mapping: the encoder must
// translate canonical IR stop reasons (OpenAI- or Anthropic-style, depending on
// the source codec) into Gemini's SCREAMING_SNAKE finishReason. tool_calls /
// tool_use become STOP — Gemini has no tool finish reason.
func TestResponseEncoderFinishReason(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"tool_calls": "STOP",
		"tool_use":   "STOP",
		"stop":       "STOP",
		"end_turn":   "STOP",
		"length":     "MAX_TOKENS",
		"max_tokens": "MAX_TOKENS",
	}
	for in, want := range cases {
		resp := ir.NewAiResponse("id", "m")
		resp.StopReason = in
		resp.ToolCalls = []ir.ToolCall{{Name: "f", Arguments: "{}"}}
		out, err := responseEncoder{}.Format(resp)
		if err != nil {
			t.Fatalf("format %q: %v", in, err)
		}
		var w response
		if err := json.Unmarshal(out, &w); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(w.Candidates) == 0 || w.Candidates[0].FinishReason != want {
			t.Errorf("StopReason %q → finishReason %q, want %q", in, w.Candidates[0].FinishReason, want)
		}
	}
}

// TestGeminiFunctionResponseWrapping guards that a tool result is always a JSON
// object in Gemini's functionResponse.response — Gemini 400s on a bare
// string/scalar. Objects pass through; scalars are wrapped as {"result": …}.
func TestGeminiFunctionResponseWrapping(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`"18C and sunny"`, `{"result":"18C and sunny"}`},
		{`{"temp":18}`, `{"temp":18}`},
		{`42`, `{"result":42}`},
	}
	for _, c := range cases {
		if got := string(geminiFunctionResponse(json.RawMessage(c.in))); got != c.want {
			t.Errorf("geminiFunctionResponse(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestThoughtSignatureEncoding guards Gemini 3's thoughtSignature requirement:
// a captured signature is re-emitted on the functionCall part, and a tool call
// without one gets the documented validation-skip sentinel so multi-turn tool
// history is accepted rather than 400'd.
func TestThoughtSignatureEncoding(t *testing.T) {
	t.Parallel()
	enc := func(sig string) string {
		req := &ir.AiRequest{Model: "m", Messages: []ir.Message{{
			Role: ir.RoleAssistant,
			Content: &ir.BlocksContent{Blocks: []ir.ContentBlock{
				&ir.ToolUseBlock{ID: "c1", Name: "get_weather", Input: json.RawMessage(`{"city":"Paris"}`), ThoughtSignature: sig},
			}},
		}}}
		out, err := requestEncoder{}.Encode(req)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return string(out.Body)
	}
	if b := enc("realsig"); !strings.Contains(b, `"thoughtSignature":"realsig"`) {
		t.Errorf("captured signature not preserved:\n%s", b)
	}
	if b := enc(""); !strings.Contains(b, `"thoughtSignature":"skip_thought_signature_validator"`) {
		t.Errorf("missing signature not backfilled with sentinel:\n%s", b)
	}
}

func TestStreamDecode(t *testing.T) {
	t.Parallel()
	d := &streamResponseDecoder{}
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
	}
	var text strings.Builder
	var sawDone bool
	var total uint32
	for _, c := range chunks {
		deltas, err := d.ParseChunk(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		for _, dl := range deltas {
			switch v := dl.(type) {
			case *ir.TextDelta:
				text.WriteString(v.Text)
			case *ir.UsageDelta:
				total = v.Usage.TotalTokens
			case *ir.DoneDelta:
				sawDone = true
				if v.StopReason != "stop" {
					t.Errorf("stop=%q", v.StopReason)
				}
			}
		}
	}
	if text.String() != "Hithere" {
		t.Errorf("text=%q", text.String())
	}
	if total != 3 {
		t.Errorf("total=%d", total)
	}
	if !sawDone {
		t.Error("no Done")
	}
}

func TestStreamEncode(t *testing.T) {
	t.Parallel()
	e := &streamResponseEncoder{}
	deltas := []ir.StreamDelta{
		&ir.TextDelta{Text: "Hi"},
		&ir.UsageDelta{Usage: ir.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
		&ir.DoneDelta{StopReason: "STOP"},
	}
	frames, err := e.FormatDeltas(deltas)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	done, _ := e.FormatDone(ir.Usage{})
	all := append(append([]codec.SSE{}, frames...), done...)

	var joined strings.Builder
	for _, f := range all {
		joined.Write(f.Bytes())
	}
	out := joined.String()
	for _, want := range []string{
		`"text":"Hi"`,
		`"finishReason":"STOP"`,
		`"totalTokenCount":3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
