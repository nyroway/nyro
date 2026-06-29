package proxy

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// MountWebui serves a built WebUI directory from the Gin engine. Static files
// are served by path; any other GET falls back to index.html (SPA routing).
// API/proxy paths (/api, /v1, /healthz) are left to their route handlers and
// return JSON 404 for unknown sub-paths. Pass an empty dir to disable.
func MountWebui(r *gin.Engine, dir string) {
	if dir == "" {
		return
	}
	index := filepath.Join(dir, "index.html")
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1") || p == "/healthz" {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "not found", "type": "gateway_error"}})
			return
		}
		if c.Request.Method == http.MethodGet {
			if full, err := filepath.Abs(filepath.Join(dir, filepath.Clean("/"+p))); err == nil {
				if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
					c.File(full)
					return
				}
			}
		}
		c.File(index) // SPA fallback for client-side routes
	})
}
