// Package storage defines the persistence layer for the Nyro gateway: the
// config-schema data model (upstreams/routes/consumers) and the CoreStorage
// interface + its typed sub-stores, and the backends (database/memory).
package storage

// ModelBalance selects the target-selection strategy for a route's upstreams.
type ModelBalance string

const (
	BalanceWeighted ModelBalance = "weighted"
	BalancePriority ModelBalance = "priority"
	BalanceCooldown ModelBalance = "cooldown"
	BalanceLatency  ModelBalance = "latency"
)

// ParseModelBalance resolves a balance string (empty → weighted).
func ParseModelBalance(s string) (ModelBalance, bool) {
	switch s {
	case "", "weighted":
		return BalanceWeighted, true
	case "priority":
		return BalancePriority, true
	case "cooldown":
		return BalanceCooldown, true
	case "latency":
		return BalanceLatency, true
	}
	return "", false
}
