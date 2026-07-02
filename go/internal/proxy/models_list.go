package proxy

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/nyroway/nyro/go/internal/webutil"
)

// handleModelsList serves GET /v1/models — the OpenAI-compatible client
// discovery endpoint. It lists the client-facing model names this gateway
// exposes, filtered by the caller's API key: open routes (enable_auth=false)
// are always listed; auth-gated routes appear only when the caller presents a
// valid, enabled, non-expired key granted access to them. Output mirrors the
// Rust proxy/handler.rs models_list: {object:"list", data:[{id, object:"model",
// created:0, owned_by:"Nyro"}]}, de-duplicated and sorted by name.
func handleModelsList(w http.ResponseWriter, r *http.Request, gw *Gateway) {
	var grantedRoutes []string
	snap := gw.snapshot()
	if raw := extractKey(r); raw != "" {
		if rec := snap.FindKey(raw); rec != nil && rec.Enabled {
			if rec.ExpiresAt == "" || !expired(rec.ExpiresAt) {
				grantedRoutes = rec.Routes
			}
		}
	}

	routes := snap.RoutesList()
	seen := map[string]struct{}{}
	var names []string
	for _, rt := range routes {
		if rt.EnableAuth {
			if !slices.Contains(grantedRoutes, rt.Model) {
				continue
			}
		}
		name := strings.TrimSpace(rt.Model)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	data := make([]map[string]any, 0, len(names))
	for _, n := range names {
		data = append(data, map[string]any{"id": n, "object": "model", "created": 0, "owned_by": "Nyro"})
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
