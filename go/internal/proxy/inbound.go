package proxy

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/proxy/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/configsync"
)

// checkAccess is the inbound access check. For open routes (EnableAuth=false)
// it always allows. Otherwise it resolves the raw token to a consumer key
// (prefix filter + hash compare against the config snapshot — raw tokens are
// never persisted), validates expiry and the route grant, then checks the
// consumer's quotas against the in-memory sliding-window counter. Returns
// (0, "", nil) to allow, or (statusCode, message, nil) to deny. When a
// concurrency quota slot was acquired, the third return is a non-nil release
// func that MUST be called exactly once when the request finishes.
func checkAccess(snap *configsync.ConfigSnapshot, qc *quota.Counter, route storage.Route, r *http.Request, consumerID *string, keyName *string) (int, string, func()) {
	if !route.EnableAuth {
		return 0, "", nil
	}
	raw := extractKey(r)
	if raw == "" {
		return http.StatusUnauthorized, "missing API key", nil
	}
	rec := snap.FindKey(raw)
	if rec == nil {
		return http.StatusUnauthorized, "invalid API key", nil
	}
	*consumerID = rec.ConsumerID
	*keyName = rec.KeyPreview
	if !rec.Enabled {
		return http.StatusForbidden, "API key is disabled", nil
	}
	if rec.ExpiresAt != "" && expired(rec.ExpiresAt) {
		return http.StatusForbidden, "API key has expired", nil
	}
	if !slices.Contains(rec.Routes, route.Model) {
		return http.StatusForbidden, "API key is not granted this route", nil
	}
	if status, msg := quotaExceeded(qc, rec); status != 0 {
		return status, msg, nil
	}
	// Concurrency quota last, so a denied window quota never leaks a slot.
	for _, q := range rec.Quotas {
		if q.QuotaType != "concurrency" {
			continue
		}
		if !qc.TryAcquire(rec.ConsumerID, q.QuotaLimit) {
			return http.StatusTooManyRequests, "consumer concurrency quota exceeded", nil
		}
		id := rec.ConsumerID
		return 0, "", func() { qc.Release(id) }
	}
	return 0, "", nil
}

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
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return false // unparseable → treat as not expired
	}
	return time.Now().After(t)
}

// quotaExceeded checks every quota attached to the consumer's key against the
// in-memory sliding counter. Limits/types/windows come from the config
// snapshot (ConsumerQuota); counts come from the per-process counter, keyed by
// (consumerID, quotaType) — token quotas count accumulated past usage and
// begin enforcing once the dispatcher records usage after a successful
// upstream response.
func quotaExceeded(qc *quota.Counter, rec *storage.ConsumerKeyAccessRecord) (int, string) {
	for _, q := range rec.Quotas {
		if q.QuotaType == "concurrency" {
			continue // enforced via TryAcquire in checkAccess, not a time window
		}
		window, err := quota.ParseWindow(q.Window)
		if err != nil {
			continue // malformed window: skip rather than block all traffic
		}
		if qc.Value(rec.ConsumerID, q.QuotaType, window) >= q.QuotaLimit {
			return http.StatusTooManyRequests, "consumer " + q.QuotaType + " quota exceeded"
		}
	}
	return 0, ""
}
