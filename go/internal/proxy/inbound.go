package proxy

import (
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/proxy/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

// checkAccess is the inbound access check. For open models (EnableAuth=false)
// it always allows. Otherwise it validates the API key, expiry, and model
// binding against the in-memory config snapshot, then checks the rpm/rpd/tpm/
// tpd quotas against the in-memory sliding-window counter (P3a: no longer reads
// request_logs). Returns (0, "") to allow, or (statusCode, message) to deny.
// Ported from proxy/dispatcher/auth.rs.
func checkAccess(snap *xds.ConfigSnapshot, qc *quota.Counter, model storage.Model, r *http.Request, apiKeyID *string, apiKeyName *string) (int, string) {
	if !model.EnableAuth {
		return 0, ""
	}
	raw := extractKey(r)
	if raw == "" {
		return http.StatusUnauthorized, "missing API key"
	}
	rec := snap.FindAPIKey(raw)
	if rec == nil {
		return http.StatusUnauthorized, "invalid API key"
	}
	*apiKeyID = rec.ID
	*apiKeyName = rec.Name
	if !rec.IsEnabled {
		return http.StatusForbidden, "API key is disabled"
	}
	if rec.ExpiresAt != "" && expired(rec.ExpiresAt) {
		return http.StatusForbidden, "API key has expired"
	}
	if !snap.ModelBindingExists(rec.ID, model.ID) {
		return http.StatusForbidden, "API key is not bound to this model"
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

// quotaExceeded checks all four quota windows against the in-memory sliding
// counter. Limits come from the API-key access record (already in the config
// snapshot); counts come from the per-process window. Token quotas (tpm/tpd)
// count accumulated past usage; they begin enforcing once the dispatcher
// records token usage into the counter (after a successful upstream response).
// Ported from auth.rs quota block.
func quotaExceeded(qc *quota.Counter, rec *storage.ApiKeyAccessRecord) (int, string) {
	if rec.RPM != nil {
		if qc.Requests(rec.ID, quota.WindowMinute) >= int64(*rec.RPM) {
			return http.StatusTooManyRequests, "api key rpm quota exceeded"
		}
	}
	if rec.RPD != nil {
		if qc.Requests(rec.ID, quota.WindowDay) >= int64(*rec.RPD) {
			return http.StatusTooManyRequests, "api key rpd quota exceeded"
		}
	}
	if rec.TPM != nil {
		if qc.Tokens(rec.ID, quota.WindowMinute) >= int64(*rec.TPM) {
			return http.StatusTooManyRequests, "api key tpm quota exceeded"
		}
	}
	if rec.TPD != nil {
		if qc.Tokens(rec.ID, quota.WindowDay) >= int64(*rec.TPD) {
			return http.StatusTooManyRequests, "api key tpd quota exceeded"
		}
	}
	return 0, ""
}
