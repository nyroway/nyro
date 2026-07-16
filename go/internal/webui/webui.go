// Package webui mounts the Go management console for the admin control plane.
package webui

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/webutil"
)

// Mount serves a built WebUI from dir, or from embedded assets when the binary
// is built with -tags webui_embed. Passing an empty dir with no embedded assets
// disables the WebUI.
func Mount(r chi.Router, dir string) {
	var fsys fs.FS
	if dir != "" {
		fsys = os.DirFS(dir)
	} else if embedded, ok := embeddedFS(); ok {
		fsys = embedded
	} else {
		return
	}
	mount(r, fsys)
}

func mount(r chi.Router, fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if isReservedPath(p) {
			webutil.Error(w, http.StatusNotFound, "not found", "GATEWAY_ERROR")
			return
		}
		if req.Method != http.MethodGet {
			webutil.Error(w, http.StatusNotFound, "not found", "GATEWAY_ERROR")
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+p), "/")
		if name != "" && serveExistingFile(fileServer, fsys, name, w, req) {
			return
		}

		serveIndex(fsys, w)
	})
}

func isReservedPath(p string) bool {
	return p == "/api" ||
		strings.HasPrefix(p, "/api/") ||
		p == "/v1" ||
		strings.HasPrefix(p, "/v1/") ||
		p == "/healthz"
}

func serveExistingFile(fileServer http.Handler, fsys fs.FS, name string, w http.ResponseWriter, req *http.Request) bool {
	info, err := fs.Stat(fsys, name)
	if err != nil || info.IsDir() {
		return false
	}
	servePath(fileServer, "/"+name, w, req)
	return true
}

func servePath(fileServer http.Handler, targetPath string, w http.ResponseWriter, req *http.Request) {
	cloned := req.Clone(req.Context())
	cloned.URL.Path = targetPath
	fileServer.ServeHTTP(w, cloned)
}

func serveIndex(fsys fs.FS, w http.ResponseWriter) {
	body, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		webutil.Error(w, http.StatusNotFound, "not found", "GATEWAY_ERROR")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}
