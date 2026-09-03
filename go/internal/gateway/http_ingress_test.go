package gateway

import (
	"net/http"
	"testing"

	httpingress "github.com/nyroway/nyro/go/internal/llm/ingress/http"
)

// newTestHandler keeps the Gateway integration suite exercising the public
// Task 9 composition boundary. Gateway itself no longer owns HTTP routes.
func newTestHandler(t testing.TB, gateway *Gateway) http.Handler {
	t.Helper()
	ingress, err := httpingress.New(gateway.Protocols, gateway, httpingress.Options{})
	if err != nil {
		t.Fatalf("compose LLM HTTP ingress: %v", err)
	}
	return ingress
}
