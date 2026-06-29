package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestReadyz verifies the readiness probe: a healthy backend returns 200 +
// "ready"; the endpoint is separate from the liveness /healthz.
func TestReadyz(t *testing.T) {
	gw := NewGateway(memory.New().Storage())
	r := NewRouter(gw)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz → %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"ready"`)) {
		t.Errorf("/readyz body: %s", rec.Body.String())
	}
}
