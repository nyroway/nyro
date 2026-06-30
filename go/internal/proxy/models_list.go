package proxy

import (
	"net/http"
	"sort"
	"strings"

	"github.com/nyroway/nyro/go/internal/web"
)

// handleModelsList serves GET /v1/models — the OpenAI-compatible client
// discovery endpoint. It lists the client-facing model names this gateway
// exposes, filtered by the caller's API key: open models (enable_auth=false)
// are always listed; auth-gated models appear only when the caller presents a
// valid, enabled, non-expired key bound to them. Output mirrors the Rust
// proxy/handler.rs models_list: {object:"list", data:[{id, object:"model",
// created:0, owned_by:"Nyro"}]}, de-duplicated and sorted by name.
func handleModelsList(w http.ResponseWriter, r *http.Request, gw *Gateway) {
	accessible := map[string]struct{}{}
	snap := gw.snapshot()
	if raw := extractKey(r); raw != "" {
		if rec := snap.FindAPIKey(raw); rec != nil && rec.IsEnabled {
			if rec.ExpiresAt == "" || !expired(rec.ExpiresAt) {
				for _, id := range snap.ListBoundModelIDs(rec.ID) {
					accessible[id] = struct{}{}
				}
			}
		}
	}

	models := snap.ModelsList()
	seen := map[string]struct{}{}
	var names []string
	for _, m := range models {
		if m.EnableAuth {
			if _, ok := accessible[m.ID]; !ok {
				continue
			}
		}
		name := strings.TrimSpace(m.Name)
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
	web.JSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
