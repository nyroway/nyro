package gateway

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
)

// checkAccess is the inbound access check. For open routes (EnableAuth=false)
// it always allows. Otherwise it resolves the raw token to a consumer key
// (prefix filter + hash compare against the config snapshot — raw tokens are
// never persisted), validates expiry and the route grant, then checks the
// consumer's quotas against quota State. Returns
// (0, "", nil) to allow, or (statusCode, message, nil) to deny. When a
// concurrency quota slot was acquired, the third return is a non-nil release
// Lease that MUST be released exactly once when the request finishes.
func checkAccess(snap *configsnapshot.Snapshot, qc *quota.Switch, route storage.Route, r *http.Request, consumerID *string, keyName *string, keyPreview *string) (int, string, quota.Lease) {
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
	// Prefer the human-readable key name; fall back to the preview so an unnamed
	// key still identifies itself in the logs. The preview is kept separately so
	// the UI can show it alongside the name.
	if rec.Name != "" {
		*keyName = rec.Name
	} else {
		*keyName = rec.KeyPreview
	}
	*keyPreview = rec.KeyPreview
	if !rec.Enabled {
		return http.StatusForbidden, "API key is disabled", nil
	}
	if rec.ExpiresAt != "" && expired(rec.ExpiresAt) {
		return http.StatusForbidden, "API key has expired", nil
	}
	if !slices.Contains(rec.Routes, route.Model) {
		return http.StatusForbidden, "API key is not granted this route", nil
	}
	if qc == nil {
		return http.StatusServiceUnavailable, "quota state unavailable", nil
	}
	if status, msg := tokenQuotaExceeded(r.Context(), qc, rec); status != 0 {
		return status, msg, nil
	}

	lease, status, msg := acquireConcurrency(r.Context(), qc, rec, concurrencyLeaseTTL(snap))
	if status != 0 {
		return status, msg, nil
	}

	limits := requestLimits(rec)
	if len(limits) > 0 {
		allowed, err := qc.AdmitRequest(r.Context(), rec.ConsumerID, limits)
		if err != nil || !allowed {
			if lease != nil {
				if releaseErr := releaseQuotaLease(lease, rec.ConsumerID); releaseErr != nil {
					return http.StatusServiceUnavailable, "quota state unavailable", nil
				}
			}
			if err != nil {
				return http.StatusServiceUnavailable, "quota state unavailable", nil
			}
			return http.StatusTooManyRequests, "consumer requests quota exceeded", nil
		}
	}
	return 0, "", lease
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

// tokenQuotaExceeded checks response-settled historical token usage. Request
// quotas use AdmitRequest after concurrency acquisition instead.
func tokenQuotaExceeded(ctx context.Context, qc *quota.Switch, rec *storage.ConsumerKeyAccessRecord) (int, string) {
	for _, q := range rec.Quotas {
		if q.QuotaType != "tokens" {
			continue
		}
		window, err := quota.ParseWindow(q.Window)
		if err != nil {
			continue
		}
		value, err := qc.Value(ctx, rec.ConsumerID, "tokens", window)
		if err != nil {
			return http.StatusServiceUnavailable, "quota state unavailable"
		}
		if value >= q.QuotaLimit {
			return http.StatusTooManyRequests, "consumer tokens quota exceeded"
		}
	}
	return 0, ""
}

func requestLimits(rec *storage.ConsumerKeyAccessRecord) []quota.RequestLimit {
	limits := make([]quota.RequestLimit, 0, len(rec.Quotas))
	for _, q := range rec.Quotas {
		if q.QuotaType != "requests" {
			continue
		}
		window, err := quota.ParseWindow(q.Window)
		if err != nil {
			continue
		}
		limits = append(limits, quota.RequestLimit{Limit: q.QuotaLimit, Window: window})
	}
	return limits
}

func acquireConcurrency(ctx context.Context, qc *quota.Switch, rec *storage.ConsumerKeyAccessRecord, leaseTTL time.Duration) (quota.Lease, int, string) {
	for _, q := range rec.Quotas {
		if q.QuotaType != "concurrency" {
			continue
		}
		lease, allowed, err := qc.Acquire(ctx, rec.ConsumerID, q.QuotaLimit, leaseTTL)
		if err != nil {
			return nil, http.StatusServiceUnavailable, "quota state unavailable"
		}
		if !allowed {
			return nil, http.StatusTooManyRequests, "consumer concurrency quota exceeded"
		}
		return lease, 0, ""
	}
	return nil, 0, ""
}

func concurrencyLeaseTTL(snap *configsnapshot.Snapshot) time.Duration {
	ttl := resolveProxySettings(snap).RequestTimeout + time.Minute
	if ttl < 5*time.Minute {
		return 5 * time.Minute
	}
	return ttl
}
