package proxy

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/web"
)

// MountWebui serves a built WebUI directory from the chi router. Static files
// are served by path; any other GET falls back to index.html (SPA routing).
// API/proxy paths (/api, /v1, /healthz) are left to their route handlers and
// return JSON 404 for unknown sub-paths. Pass an empty dir to disable.
func MountWebui(r chi.Router, dir string) {
	if dir == "" {
		return
	}
	index := filepath.Join(dir, "index.html")
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1") || p == "/healthz" {
			web.Error(w, http.StatusNotFound, "not found", "gateway_error")
			return
		}
		if r.Method == http.MethodGet {
			if full, err := filepath.Abs(filepath.Join(dir, filepath.Clean("/"+p))); err == nil {
				if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
					http.ServeFile(w, r, full)
					return
				}
			}
		}
		http.ServeFile(w, r, index) // SPA fallback for client-side routes
	})
}
