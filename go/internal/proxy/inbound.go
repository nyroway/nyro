package proxy

import (
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// checkAccess is the inbound access check. For open models (EnableAuth=false)
// it always allows. Otherwise it validates the API key, expiry, model binding,
// and the rpm/rpd/tpm/tpd quotas. Returns (0, "") to allow, or (statusCode,
// message) to deny. Ported from proxy/dispatcher/auth.rs.
func checkAccess(s storage.Storage, model storage.Model, r *http.Request, apiKeyID *string) (int, string) {
	if !model.EnableAuth {
		return 0, ""
	}
	raw := extractKey(r)
	if raw == "" {
		return http.StatusUnauthorized, "missing API key"
	}
	rec, err := s.Auth().FindAPIKey(raw)
	if err != nil || rec == nil {
		return http.StatusUnauthorized, "invalid API key"
	}
	*apiKeyID = rec.ID
	if !rec.IsEnabled {
		return http.StatusForbidden, "API key is disabled"
	}
	if rec.ExpiresAt != "" && expired(rec.ExpiresAt) {
		return http.StatusForbidden, "API key has expired"
	}
	if bound, _ := s.Auth().ModelBindingExists(rec.ID, model.ID); !bound {
		return http.StatusForbidden, "API key is not bound to this model"
	}
	if status, msg := quotaExceeded(s, rec); status != 0 {
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

// quotaExceeded checks all four quota windows. Token quotas (tpm/tpd) count
// accumulated past usage from the request log; they begin enforcing once token
// accounting is captured into RequestLog. Ported from auth.rs quota block.
func quotaExceeded(s storage.Storage, rec *storage.ApiKeyAccessRecord) (int, string) {
	if rec.RPM != nil {
		if n, _ := s.Auth().RequestCountSince(rec.ID, storage.WindowMinute); n >= int64(*rec.RPM) {
			return http.StatusTooManyRequests, "api key rpm quota exceeded"
		}
	}
	if rec.RPD != nil {
		if n, _ := s.Auth().RequestCountSince(rec.ID, storage.WindowDay); n >= int64(*rec.RPD) {
			return http.StatusTooManyRequests, "api key rpd quota exceeded"
		}
	}
	if rec.TPM != nil {
		if n, _ := s.Auth().TokenCountSince(rec.ID, storage.WindowMinute); n >= int64(*rec.TPM) {
			return http.StatusTooManyRequests, "api key tpm quota exceeded"
		}
	}
	if rec.TPD != nil {
		if n, _ := s.Auth().TokenCountSince(rec.ID, storage.WindowDay); n >= int64(*rec.TPD) {
			return http.StatusTooManyRequests, "api key tpd quota exceeded"
		}
	}
	return 0, ""
}
