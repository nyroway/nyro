package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMountServesDirectorySpaAndKeepsAPIsJSON404(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("spa-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("static-asset"), 0o644); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	Mount(router, dir)

	req := httptest.NewRequest(http.MethodGet, "/asset.txt", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "static-asset" {
		t.Fatalf("asset body = %q, want static-asset", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/providers", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spa status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "spa-index" {
		t.Fatalf("spa body = %q, want spa-index", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("api status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, "gateway_error") {
		t.Fatalf("api body = %q, want gateway_error JSON", got)
	}
}

// TestMountDisabledLeavesDefaultNotFound verifies that Mount(r, "") with no
// embedded assets (this repo's default build, no webui_embed tag) does not
// register a custom NotFound handler at all. The router must fall back to
// chi's own default 404 behavior instead of the WebUI package's JSON
// gateway_error body or an SPA index.html fallback.
func TestMountDisabledLeavesDefaultNotFound(t *testing.T) {
	router := chi.NewRouter()
	Mount(router, "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "gateway_error") {
		t.Fatalf("body = %q, want chi's default 404 body, not the webui package's custom NotFound handler", body)
	}
	if !strings.Contains(body, "404 page not found") {
		t.Fatalf("body = %q, want chi's default \"404 page not found\" text", body)
	}
}
