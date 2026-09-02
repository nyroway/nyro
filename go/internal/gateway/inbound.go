package gateway

import (
	"net/http"
	"strings"
	"time"
)

// extractKey pulls the inbound API key from Authorization: Bearer, x-api-key,
// or x-goog-api-key (Gemini native clients). Ported from proxy/security.rs.
func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if h := r.Header.Get("X-Api-Key"); h != "" {
		return h
	}
	if h := r.Header.Get("X-Goog-Api-Key"); h != "" {
		return h
	}
	return ""
}

func expired(iso string) bool {
	expiresAt, err := time.Parse(time.RFC3339, iso)
	return err == nil && time.Now().After(expiresAt)
}
