package proxy

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMountWebui(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644)

	r := chi.NewRouter()
	MountWebui(r, dir)

	mustContain := func(method, path, want string, wantCode int) {
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != wantCode || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s %s → %d %q, want %d containing %q", method, path, rec.Code, rec.Body.String(), wantCode, want)
		}
	}

	mustContain("GET", "/", "SPA", 200)                      // root → index.html
	mustContain("GET", "/assets/app.js", "console.log", 200) // static asset
	mustContain("GET", "/providers", "SPA", 200)             // SPA route → index fallback
	mustContain("GET", "/api/v1/nope", "gateway_error", 404) // API 404 (not index)
}
