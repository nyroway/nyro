package storage

import (
	"fmt"
	"regexp"

	"github.com/nyroway/nyro/go/internal/proxy/quota"
)

// Consumer is an API consumer that owns keys, route grants, and quotas
// (tables: consumers / consumer_keys / consumer_routes / consumer_quotas). It
// replaces the legacy ApiKey: a single consumer can hold multiple keys and
// grants routes (model names) that apply to all of its keys.
type Consumer struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Enabled bool          `json:"enabled"`
	Keys    []ConsumerKey `json:"keys,omitempty"`
	// Routes are the granted route model names. An empty/nil slice means
	// access to all routes is allowed (default-allow), matching Protocols
	// and IPAllowlist.
	Routes      []string          `json:"routes,omitempty"`
	Quotas      []ConsumerQuota   `json:"quotas,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Protocols   []string          `json:"protocols,omitempty"`
	IPAllowlist []string          `json:"ip_allowlist,omitempty"`
	Limits      *ConsumerLimits   `json:"limits,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

// ConsumerLimits caps per-request resource usage for a consumer. A zero value
// on any field means "no limit" for that dimension.
type ConsumerLimits struct {
	MaxInputTokens      int64 `json:"max_input_tokens,omitempty"`
	MaxOutputTokens     int64 `json:"max_output_tokens,omitempty"`
	MaxRequestBodyBytes int64 `json:"max_request_body_bytes,omitempty"`
}

// ConsumerKey is one credential owned by a consumer (table: consumer_keys).
// Only KeyPreview + KeyHash are persisted; the raw token is held only at
// creation/import time and never stored.
type ConsumerKey struct {
	ID         string `json:"id"`
	ConsumerID string `json:"consumer_id"`
	Name       string `json:"name"`
	KeyPreview string `json:"key_preview"`
	KeyHash    string `json:"-"` // never serialized
	// Token carries the raw key exactly once, in the response to the create
	// call that generated it. It is never populated on read paths (List/Get)
	// and never persisted.
	Token      string `json:"token,omitempty"`
	Enabled    bool   `json:"enabled"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// ConsumerQuota is one quota attached to a consumer (table: consumer_quotas).
type ConsumerQuota struct {
	ID         string `json:"id"`
	ConsumerID string `json:"consumer_id"`
	QuotaType  string `json:"quota_type"` // requests | tokens | concurrency | budget
	QuotaLimit int64  `json:"quota_limit"`
	Window     string `json:"window,omitempty"`
	// Currency is set only for budget quotas (ISO 4217 code, e.g. "USD").
	// Budgets are validated and persisted but not enforced by the proxy yet.
	Currency string `json:"currency,omitempty"`
}

// CreateConsumer is the write DTO for creating a consumer with its keys, route
// grants, and quotas in one call.
type CreateConsumer struct {
	Name        string                `json:"name"`
	Enabled     *bool                 `json:"enabled,omitempty"`
	Keys        []CreateConsumerKey   `json:"keys,omitempty"`
	Routes      []string              `json:"routes,omitempty"`
	Quotas      []CreateConsumerQuota `json:"quotas,omitempty"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
	Protocols   []string              `json:"protocols,omitempty"`
	IPAllowlist []string              `json:"ip_allowlist,omitempty"`
	Limits      *ConsumerLimits       `json:"limits,omitempty"`
}

// CreateConsumerKey carries the raw token at creation time; the store derives
// KeyPreview + KeyHash from it and discards the plaintext.
type CreateConsumerKey struct {
	Name      string `json:"name"`
	Token     string `json:"token,omitempty"` // raw; empty = auto-generate
	ExpiresAt string `json:"expires_at,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// CreateConsumerQuota is one quota within a CreateConsumer.
type CreateConsumerQuota struct {
	QuotaType  string `json:"quota_type"`
	QuotaLimit int64  `json:"quota_limit"`
	Window     string `json:"window,omitempty"`
	// Currency is required when QuotaType is "budget".
	Currency string `json:"currency,omitempty"`
}

// UpdateConsumerKey is the partial-update DTO for a single consumer key;
// nil fields mean unchanged. ExpiresAt: nil = unchanged, "" = clear to never-expires.
type UpdateConsumerKey struct {
	Name      *string `json:"name,omitempty"`
	Enabled   *bool   `json:"enabled,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// UpdateConsumer is the partial-update DTO; nil fields mean "unchanged". A
// non-nil Quotas, Routes, Protocols, or IPAllowlist slice replaces that
// dimension wholesale (matching UpdateRoute.Upstreams semantics). Key
// mutations go through a dedicated sub-store in a later step.
type UpdateConsumer struct {
	Name        *string                `json:"name,omitempty"`
	Enabled     *bool                  `json:"enabled,omitempty"`
	Quotas      *[]CreateConsumerQuota `json:"quotas,omitempty"`
	Routes      *[]string              `json:"routes,omitempty"`
	Metadata    *map[string]string     `json:"metadata,omitempty"`
	Protocols   *[]string              `json:"protocols,omitempty"`
	IPAllowlist *[]string              `json:"ip_allowlist,omitempty"`
	Limits      *ConsumerLimits        `json:"limits,omitempty"`
}

// validQuotaTypes enumerates the allowed ConsumerQuota.QuotaType values.
var validQuotaTypes = map[string]bool{"requests": true, "tokens": true, "concurrency": true, "budget": true}

// naturalMonthWindow matches an "Nmo" budget window (e.g. "1mo", "3mo"): N
// natural calendar months, which have no fixed time.Duration.
var naturalMonthWindow = regexp.MustCompile(`^[0-9]+mo$`)

// ValidateConsumerQuota checks a single quota DTO's invariants:
//   - QuotaType must be one of requests, tokens, concurrency, budget.
//   - QuotaLimit must be positive.
//   - concurrency quotas must not set a window (they aren't time-windowed;
//     they cap concurrently in-flight requests).
//   - budget quotas must set Currency, and their window may additionally be
//     "Nmo" (N natural calendar months, e.g. "1mo", "3mo") on top of the
//     s/m/h/d units accepted elsewhere; "Nmo" has no fixed time.Duration, so
//     it is accepted as a literal here and left for the (not-yet-built)
//     budget-enforcement path to interpret against calendar boundaries.
//     Budgets are validated and persisted only; they are not enforced by the
//     proxy in this version.
//   - any other window must parse via quota.ParseWindow (the same parser the
//     proxy's quota counter uses at enforcement time).
func ValidateConsumerQuota(q CreateConsumerQuota) error {
	if !validQuotaTypes[q.QuotaType] {
		return fmt.Errorf("invalid quota_type %q: must be one of requests, tokens, concurrency, budget", q.QuotaType)
	}
	if q.QuotaLimit <= 0 {
		return fmt.Errorf("quota_limit must be > 0, got %d", q.QuotaLimit)
	}
	if q.QuotaType == "concurrency" {
		if q.Window != "" {
			return fmt.Errorf("concurrency quotas must not set a window")
		}
		return nil
	}
	if q.QuotaType == "budget" {
		if q.Currency == "" {
			return fmt.Errorf("budget quotas require a currency")
		}
		if q.Window != "" && !naturalMonthWindow.MatchString(q.Window) {
			if _, err := quota.ParseWindow(q.Window); err != nil {
				return fmt.Errorf("invalid window %q: %w", q.Window, err)
			}
		}
		return nil
	}
	if q.Window != "" {
		if _, err := quota.ParseWindow(q.Window); err != nil {
			return fmt.Errorf("invalid window %q: %w", q.Window, err)
		}
	}
	return nil
}

// ConsumerKeyAccessRecord is the inbound-auth read model: the result of looking
// up a consumer key by its raw token. It carries the consumer's route grants
// and quotas so the proxy can authorize and rate-limit a request in one shot.
type ConsumerKeyAccessRecord struct {
	KeyID      string          `json:"key_id"`
	ConsumerID string          `json:"consumer_id"`
	KeyPreview string          `json:"key_preview"`
	Enabled    bool            `json:"enabled"`
	ExpiresAt  string          `json:"expires_at,omitempty"`
	Routes     []string        `json:"routes,omitempty"`
	Quotas     []ConsumerQuota `json:"quotas,omitempty"`
}

// ConsumerStore is the CRUD store for consumers (with nested keys/routes/quotas).
type ConsumerStore interface {
	List() ([]Consumer, error)
	Get(id string) (*Consumer, error)
	ByName(name string) (*Consumer, error)
	Create(in CreateConsumer) (Consumer, error)
	Update(id string, in UpdateConsumer) (Consumer, error)
	Delete(id string) error

	// AddKey creates a new key for consumerID, returning it with the
	// one-time raw Token populated (mirroring Create's key-creation
	// semantics). Rotation is a caller-side AddKey + DeleteKey composition;
	// this store does not implement rotation itself.
	AddKey(consumerID string, in CreateConsumerKey) (ConsumerKey, error)
	// UpdateKey partially updates a single key by its own ID (not the owning
	// consumer's ID). The returned ConsumerKey never carries a Token.
	UpdateKey(keyID string, in UpdateConsumerKey) (ConsumerKey, error)
	// DeleteKey deletes a single key by its own ID.
	DeleteKey(keyID string) error
}

// KeyAuthStore is the inbound-auth read path used by the proxy: resolve a raw
// token to its consumer key + grants. Implementations use KeyPreview filtering
// plus a hash compare (raw tokens are not persisted); the contract is defined
// here, the implementation is added in a later step.
type KeyAuthStore interface {
	FindKey(rawKey string) (*ConsumerKeyAccessRecord, error)
}
