package gateway

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/pipeline"
)

// TestServeStreamHugeSSELine verifies SSE lines larger than bufio.Scanner's
// 1MiB default cap still stream through (truncation regression).
func TestServeStreamHugeSSELine(t *testing.T) {
	catalog := testProtocolCatalog(t)
	ingress, ok := catalog.Ingress(protocol.OpenAIChatCompletionsV1)
	if !ok {
		t.Fatal("openai ingress codec missing")
	}
	chatIngress, ok := ingress.(protocol.ChatIngressCodec)
	if !ok {
		t.Fatalf("openai ingress codec type = %T", ingress)
	}
	egress, ok := catalog.Egress(protocol.OpenAIChatCompletionsV1)
	if !ok {
		t.Fatal("openai egress codec missing")
	}
	chatEgress, ok := egress.(protocol.ChatEgressCodec)
	if !ok {
		t.Fatalf("openai egress codec type = %T", egress)
	}

	// One valid OpenAI chunk whose content pushes the data: line past 2 MiB.
	big := strings.Repeat("x", 2<<20)
	chunk := `{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o",` +
		`"choices":[{"index":0,"delta":{"content":"` + big + `"},"finish_reason":null}]}`
	upstream := bytes.NewBufferString("data: " + chunk + "\n\ndata: [DONE]\n\n")

	g := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	rec := httptest.NewRecorder()
	ex := &pipeline.Exchange{Ctx: context.Background(), W: rec}
	g.serveStream(ex, upstream, chatEgress, chatIngress)

	if !strings.Contains(rec.Body.String(), big[:64]) {
		t.Errorf("streamed body lost the oversized chunk content; got %d bytes", rec.Body.Len())
	}
}
