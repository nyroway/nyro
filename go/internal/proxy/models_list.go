package proxy

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// handleModelsList serves GET /v1/models — the OpenAI-compatible client
// discovery endpoint. It lists the client-facing model names this gateway
// exposes, filtered by the caller's API key: open models (enable_auth=false)
// are always listed; auth-gated models appear only when the caller presents a
// valid, enabled, non-expired key bound to them. Output mirrors the Rust
// proxy/handler.rs models_list: {object:"list", data:[{id, object:"model",
// created:0, owned_by:"Nyro"}]}, de-duplicated and sorted by name.
func handleModelsList(c *gin.Context, gw *Gateway) {
	accessible := map[string]struct{}{}
	if raw := extractKey(c.Request); raw != "" {
		if rec, err := gw.Storage.Auth().FindAPIKey(raw); err == nil && rec != nil && rec.IsEnabled {
			if rec.ExpiresAt == "" || !expired(rec.ExpiresAt) {
				if bound, err := gw.Storage.Auth().ListBoundModelIDs(rec.ID); err == nil {
					for _, id := range bound {
						accessible[id] = struct{}{}
					}
				}
			}
		}
	}

	models, _ := gw.Storage.Models().List()
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

	data := make([]gin.H, 0, len(names))
	for _, n := range names {
		data = append(data, gin.H{"id": n, "object": "model", "created": 0, "owned_by": "Nyro"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
