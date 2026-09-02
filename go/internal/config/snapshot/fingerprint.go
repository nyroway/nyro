package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const (
	keyPreviewLeadLen  = 9
	keyPreviewTrailLen = 6
)

func previewOf(raw string) string {
	if len(raw) <= keyPreviewLeadLen+keyPreviewTrailLen {
		return raw
	}
	return raw[:keyPreviewLeadLen] + raw[len(raw)-keyPreviewTrailLen:]
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns a deterministic hash of the effective data-plane
// configuration. It deliberately excludes runtime state and contains only
// the existing credential representation, never raw consumer keys.
func (s *Snapshot) Fingerprint() string {
	if s == nil {
		return ""
	}
	type key struct {
		ConsumerAccess
		KeyHash string
	}
	type setting struct {
		Key   string
		Value string
	}
	payload := struct {
		Upstreams []Upstream
		Routes    []Route
		Keys      []key
		Settings  []setting
	}{
		Upstreams: s.UpstreamsList(),
		Routes:    s.RoutesList(),
	}
	for _, entries := range s.keysByPreview {
		for _, entry := range entries {
			payload.Keys = append(payload.Keys, key{ConsumerAccess: cloneConsumerAccess(entry.ConsumerAccess), KeyHash: entry.keyHash})
		}
	}
	for key, value := range s.settings {
		payload.Settings = append(payload.Settings, setting{Key: key, Value: value})
	}
	sort.Slice(payload.Upstreams, func(i, j int) bool { return payload.Upstreams[i].ID < payload.Upstreams[j].ID })
	sort.Slice(payload.Routes, func(i, j int) bool { return payload.Routes[i].Model < payload.Routes[j].Model })
	for i := range payload.Routes {
		sort.Slice(payload.Routes[i].Upstreams, func(a, b int) bool {
			left, right := payload.Routes[i].Upstreams[a], payload.Routes[i].Upstreams[b]
			if left.ID != right.ID {
				return left.ID < right.ID
			}
			if left.RouteID != right.RouteID {
				return left.RouteID < right.RouteID
			}
			if left.UpstreamID != right.UpstreamID {
				return left.UpstreamID < right.UpstreamID
			}
			if left.Model != right.Model {
				return left.Model < right.Model
			}
			if left.Weight != right.Weight {
				return left.Weight < right.Weight
			}
			if left.Priority != right.Priority {
				return left.Priority < right.Priority
			}
			return !left.Enabled && right.Enabled
		})
	}
	for i := range payload.Keys {
		sort.Strings(payload.Keys[i].Routes)
		sort.Slice(payload.Keys[i].Quotas, func(a, b int) bool {
			left, right := payload.Keys[i].Quotas[a], payload.Keys[i].Quotas[b]
			if left.ID != right.ID {
				return left.ID < right.ID
			}
			if left.ConsumerID != right.ConsumerID {
				return left.ConsumerID < right.ConsumerID
			}
			if left.QuotaType != right.QuotaType {
				return left.QuotaType < right.QuotaType
			}
			if left.QuotaLimit != right.QuotaLimit {
				return left.QuotaLimit < right.QuotaLimit
			}
			return left.Window < right.Window
		})
	}
	sort.Slice(payload.Keys, func(i, j int) bool {
		left, _ := json.Marshal(payload.Keys[i])
		right, _ := json.Marshal(payload.Keys[j])
		return bytes.Compare(left, right) < 0
	})
	sort.Slice(payload.Settings, func(i, j int) bool { return payload.Settings[i].Key < payload.Settings[j].Key })
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
