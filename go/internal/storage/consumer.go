package storage

// Consumer is an API consumer that owns keys, route grants, and quotas
// (tables: consumers / consumer_keys / consumer_routes / consumer_quotas). It
// replaces the legacy ApiKey: a single consumer can hold multiple keys and
// grants routes (model names) that apply to all of its keys.
type Consumer struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	Keys      []ConsumerKey   `json:"keys,omitempty"`
	Routes    []string        `json:"routes,omitempty"` // route model names granted
	Quotas    []ConsumerQuota `json:"quotas,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

// ConsumerKey is one credential owned by a consumer (table: consumer_keys).
// Only KeyPrefix + KeyHash are persisted; the raw token is held only at
// creation/import time and never stored.
type ConsumerKey struct {
	ID         string `json:"id"`
	ConsumerID string `json:"consumer_id"`
	Name       string `json:"name"`
	KeyPrefix  string `json:"key_prefix"`
	KeyHash    string `json:"-"` // never serialized
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
	QuotaType  string `json:"quota_type"` // requests | tokens | concurrency
	QuotaLimit int64  `json:"quota_limit"`
	Window     string `json:"window,omitempty"`
}

// CreateConsumer is the write DTO for creating a consumer with its keys, route
// grants, and quotas in one call.
type CreateConsumer struct {
	Name    string                `json:"name"`
	Enabled *bool                 `json:"enabled,omitempty"`
	Keys    []CreateConsumerKey   `json:"keys,omitempty"`
	Routes  []string              `json:"routes,omitempty"`
	Quotas  []CreateConsumerQuota `json:"quotas,omitempty"`
}

// CreateConsumerKey carries the raw token at creation time; the store derives
// KeyPrefix + KeyHash from it and discards the plaintext.
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
}

// UpdateConsumer is the partial-update DTO; nil fields mean "unchanged".
// (Key/Route/Quota mutations go through dedicated sub-stores in a later step.)
type UpdateConsumer struct {
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

// ConsumerKeyAccessRecord is the inbound-auth read model: the result of looking
// up a consumer key by its raw token. It carries the consumer's route grants
// and quotas so the proxy can authorize and rate-limit a request in one shot.
type ConsumerKeyAccessRecord struct {
	KeyID      string          `json:"key_id"`
	ConsumerID string          `json:"consumer_id"`
	KeyPrefix  string          `json:"key_prefix"`
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
}

// KeyAuthStore is the inbound-auth read path used by the proxy: resolve a raw
// token to its consumer key + grants. Implementations use KeyPrefix filtering
// plus a hash compare (raw tokens are not persisted); the contract is defined
// here, the implementation is added in a later step.
type KeyAuthStore interface {
	FindKey(rawKey string) (*ConsumerKeyAccessRecord, error)
}
