// Package web provides the small set of net/http JSON helpers used across the
// nyro HTTP layer (admin API + proxy), replacing gin's c.JSON / c.ShouldBind.
package web

import (
	"encoding/json"
	"net/http"
)

// JSON encodes v as JSON and writes it with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a gateway-style error envelope: {"error":{"message":...,"type":...}}.
func Error(w http.ResponseWriter, status int, message, errType string) {
	JSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": errType}})
}

// Decode reads a JSON request body into v.
func Decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
