package ir

// CacheTtl is a prompt-cache time-to-live hint. The default is 5 minutes.
// Ported from CacheTtl (serde rename_all = "snake_case").
type CacheTtl string

const (
	CacheTtlEphemeral5m CacheTtl = "ephemeral_5m"
	CacheTtlEphemeral1h CacheTtl = "ephemeral_1h"
)

// CacheControl is a per-block cache breakpoint, placed by the ingress decoder
// when the client requests a cache position. The encoder translates it into
// the target protocol's wire format.
type CacheControl struct {
	Ttl                CacheTtl
	BreakpointPriority uint8
}

// EphemeralCache returns the default 5-minute cache breakpoint.
func EphemeralCache() CacheControl { return CacheControl{Ttl: CacheTtlEphemeral5m} }

// Ephemeral1hCache returns the 1-hour cache breakpoint.
func Ephemeral1hCache() CacheControl { return CacheControl{Ttl: CacheTtlEphemeral1h} }
