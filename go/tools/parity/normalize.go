// Package parity provides normalization utilities for stable response
// diffing — the foundation of the Rust-vs-Go parity harness (P5). Volatile
// fields (generated ids, timestamps, fingerprints) are dropped and object keys
// are sorted, so two behaviorally-equivalent responses compare equal.
package parity

import "encoding/json"

// volatileFields are dropped before comparison (generated ids, timestamps,
// provider fingerprints — values that vary between runs/backends).
var volatileFields = map[string]bool{
	"id":                 true,
	"created":            true,
	"created_at":         true,
	"updated_at":         true,
	"system_fingerprint": true,
	"last_test_at":       true,
	"last_refresh_at":    true,
	"expires_at":         true,
}

// NormalizeJSON parses input, drops volatile fields recursively, and
// re-marshals with sorted keys to a canonical byte string.
func NormalizeJSON(input []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return nil, err
	}
	return json.Marshal(normalize(v))
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if volatileFields[k] {
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	}
	return v
}
