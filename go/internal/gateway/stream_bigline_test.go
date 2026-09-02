package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeStreamHugeSSELine verifies SSE lines larger than bufio.Scanner's
// 1MiB default cap still stream through (truncation regression).
func TestServeStreamHugeSSELine(t *testing.T) {
	// One valid OpenAI chunk whose content pushes the data: line past 2 MiB.
	big := strings.Repeat("x", 2<<20)
	chunk := `{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o",` +
		`"choices":[{"index":0,"delta":{"content":"` + big + `"},"finish_reason":null}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: "+chunk+"\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	engine := NewRouter(newTestGateway(t, upstream.URL))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`,
	))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, request)

	if !strings.Contains(rec.Body.String(), big[:64]) {
		t.Errorf("streamed body lost the oversized chunk content; got %d bytes", rec.Body.Len())
	}
}
