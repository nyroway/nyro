package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

func TestRegistryHasAnthropic(t *testing.T) {
	t.Parallel()
	h, ok := codec.Get(ids.AnthropicMessages20230601)
	if !ok {
		t.Fatal("Anthropic messages handler not registered")
	}
	if h.Endpoint() != ids.AnthropicMessages20230601 {
		t.Errorf("endpoint mismatch: %v", h.Endpoint())
	}
}

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	in := `{"model":"claude-3-5-sonnet","max_tokens":100,"system":"be brief",` +
		`"messages":[{"role":"user","content":"hi"}],"temperature":0.5,"stream":true,` +
		`"tools":[{"name":"get_weather","description":"w","input_schema":{"type":"object"}}]}`

	req, err := requestDecoder{}.Decode([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "claude-3-5-sonnet" {
		t.Errorf("model=%q", req.Model)
	}
	if req.System != "be brief" {
		t.Errorf("system=%q", req.System)
	}
	if len(req.Messages) != 1 || ir.ToText(req.Messages[0].Content) != "hi" {
		t.Errorf("messages=%+v", req.Messages)
	}
	if req.Generation.MaxTokens == nil || *req.Generation.MaxTokens != 100 {
		t.Errorf("max_tokens=%v", req.Generation.MaxTokens)
	}
	if !req.Stream.Enabled {
		t.Error("stream should be enabled")
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Errorf("tools=%+v", req.Tools)
	}

	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if out.Path != "/v1/messages" {
		t.Errorf("path=%q", out.Path)
	}
	if out.Headers["anthropic-version"] != "2023-06-01" {
		t.Errorf("missing anthropic-version header: %+v", out.Headers)
	}
	var w request
	if err := json.Unmarshal(out.Body, &w); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if w.Model != "claude-3-5-sonnet" || w.MaxTokens != 100 {
		t.Errorf("wire round-trip wrong: %+v", w)
	}
	if s := decodeSystem(w.System); s != "be brief" {
		t.Errorf("system round-trip=%q", s)
	}
}

func TestNonStreamResponse(t *testing.T) {
	t.Parallel()
	body := `{"id":"m1","type":"message","role":"assistant","model":"claude",` +
		`"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":3,"output_tokens":2}}`

	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello" || resp.StopReason != "end_turn" || resp.Usage.TotalTokens != 5 {
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
	if w.Type != "message" || w.Role != "assistant" || len(w.Content) != 1 {
		t.Errorf("wire=%+v", w)
	}
}

func TestStreamDecode(t *testing.T) {
	t.Parallel()
	d := &streamResponseDecoder{}
	events := []string{
		`{"type":"message_start","message":{"id":"m1","model":"claude","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	var text strings.Builder
	var sawStart, sawDone bool
	var total uint32
	for _, e := range events {
		deltas, err := d.ParseChunk(e)
		if err != nil {
			t.Fatalf("parse %q: %v", e, err)
		}
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
				if v.StopReason != "end_turn" {
					t.Errorf("stop=%q", v.StopReason)
				}
			}
		}
	}
	if !sawStart {
		t.Error("no MessageStart")
	}
	if text.String() != "Hi" {
		t.Errorf("text=%q", text.String())
	}
	if total != 7 {
		t.Errorf("total tokens=%d, want 7", total)
	}
	if !sawDone {
		t.Error("no Done")
	}
}

func TestStreamEncode(t *testing.T) {
	t.Parallel()
	e := &streamResponseEncoder{}
	deltas := []ir.StreamDelta{
		&ir.MessageStartDelta{ID: "m1", Model: "claude"},
		&ir.TextDelta{Text: "Hi"},
		&ir.UsageDelta{Usage: ir.Usage{CompletionTokens: 2}},
		&ir.DoneDelta{StopReason: "end_turn"},
	}
	frames, err := e.FormatDeltas(deltas)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	joined := joinFrames(frames)
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		`"text":"Hi"`,
		"event: content_block_stop",
		"event: message_delta",
		`"stop_reason":"end_turn"`,
		"event: message_stop",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in stream:\n%s", want, joined)
		}
	}
}

func joinFrames(frames []codec.SSE) string {
	var sb strings.Builder
	for _, f := range frames {
		sb.Write(f.Bytes())
	}
	return sb.String()
}
