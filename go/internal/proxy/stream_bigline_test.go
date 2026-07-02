package proxy

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// TestServeStreamHugeSSELine verifies SSE lines larger than bufio.Scanner's
// 1MiB default cap still stream through (truncation regression).
func TestServeStreamHugeSSELine(t *testing.T) {
	h, ok := codec.Get(ids.OpenAICompatibleChatCompletionsV1)
	if !ok {
		t.Fatal("openai codec not registered")
	}

	// One valid OpenAI chunk whose content pushes the data: line past 2 MiB.
	big := strings.Repeat("x", 2<<20)
	chunk := `{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o",` +
		`"choices":[{"index":0,"delta":{"content":"` + big + `"},"finish_reason":null}]}`
	upstream := bytes.NewBufferString("data: " + chunk + "\n\ndata: [DONE]\n\n")

	g := NewGateway()
	rec := httptest.NewRecorder()
	var usage ir.Usage
	g.serveStream(context.Background(), rec, upstream, h, h, &usage, plugin.NewContextBag())

	if !strings.Contains(rec.Body.String(), big[:64]) {
		t.Errorf("streamed body lost the oversized chunk content; got %d bytes", rec.Body.Len())
	}
}
