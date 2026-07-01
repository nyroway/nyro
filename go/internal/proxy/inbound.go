package proxy

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/proxy/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

// checkAccess is the inbound access check. For open routes (EnableAuth=false)
// it always allows. Otherwise it resolves the raw token to a consumer key
// (prefix filter + hash compare against the config snapshot — raw tokens are
// never persisted), validates expiry and the route grant, then checks the
// consumer's quotas against the in-memory sliding-window counter. Returns
// (0, "") to allow, or (statusCode, message) to deny.
func checkAccess(snap *xds.ConfigSnapshot, qc *quota.Counter, route storage.Route, r *http.Request, consumerID *string, keyName *string) (int, string) {
	if !route.EnableAuth {
		return 0, ""
	}
	raw := extractKey(r)
	if raw == "" {
		return http.StatusUnauthorized, "missing API key"
	}
	rec := snap.FindKey(raw)
	if rec == nil {
		return http.StatusUnauthorized, "invalid API key"
	}
	*consumerID = rec.ConsumerID
	*keyName = rec.KeyPrefix
	if !rec.Enabled {
		return http.StatusForbidden, "API key is disabled"
	}
	if rec.ExpiresAt != "" && expired(rec.ExpiresAt) {
		return http.StatusForbidden, "API key has expired"
	}
	if !slices.Contains(rec.Routes, route.Model) {
		return http.StatusForbidden, "API key is not granted this route"
	}
	if status, msg := quotaExceeded(qc, rec); status != 0 {
		return status, msg
	}
	return 0, ""
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
