package router

import (
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// Key identifies one upstream target for health/latency tracking.
type Key string

// KeyOf builds the tracking key for a backend (providerID:model).
func KeyOf(b storage.RouteUpstream) Key { return Key(b.UpstreamID + ":" + b.Model) }

type healthEntry struct {
	consecFail    int
	cooldownUntil time.Time
}

// Router selects an upstream backend among a model's targets and records
// per-backend health + latency after each attempt.
type Router struct {
	mu       sync.RWMutex
	health   map[Key]*healthEntry
	lastUsed map[Key]time.Time
	latency  map[Key]float64 // EMA latency in ms
}

// New builds an empty Router.
func New() *Router {
	return &Router{
		health:   map[Key]*healthEntry{},
		lastUsed: map[Key]time.Time{},
		latency:  map[Key]float64{},
	}
}

// Select returns the model's backends ordered best-first, skipping those in
// cooldown. If every backend is in cooldown it falls back to all of them (the
// gateway never hard-fails solely due to cooldown). Ordering honours the
// model's balance strategy.
func (r *Router) Select(targets []storage.RouteUpstream, balance storage.ModelBalance) []storage.RouteUpstream {
	if len(targets) <= 1 {
		return targets
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now()
	healthy := make([]storage.RouteUpstream, 0, len(targets))
	for _, b := range targets {
		if h, ok := r.health[KeyOf(b)]; ok && now.Before(h.cooldownUntil) {
			continue
		}
		healthy = append(healthy, b)
	}
	if len(healthy) == 0 {
		healthy = append(healthy, targets...) // all cooling → try anyway
	}

	switch balance {
	case storage.BalancePriority:
		sort.SliceStable(healthy, func(i, j int) bool { return healthy[i].Priority < healthy[j].Priority })
	case storage.BalanceLatency:
		sort.SliceStable(healthy, func(i, j int) bool {
			return r.latency[KeyOf(healthy[i])] < r.latency[KeyOf(healthy[j])]
		})
	case storage.BalanceCooldown:
		// Least-recently-used first.
		sort.SliceStable(healthy, func(i, j int) bool {
			return r.lastUsed[KeyOf(healthy[i])].Before(r.lastUsed[KeyOf(healthy[j])])
		})
	case storage.BalanceWeighted, "":
		healthy = weightedOrder(healthy)
	}
	return healthy
}

// Record updates health, last-used, and EMA latency after an attempt.
func (r *Router) Record(k Key, success bool, latencyMs float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.lastUsed[k] = now

	prev := r.latency[k]
	if prev == 0 || latencyMs <= 0 {
		if latencyMs > 0 {
			r.latency[k] = latencyMs
		}
	} else {
		r.latency[k] = prev*0.7 + latencyMs*0.3 // EMA
	}

	h := r.health[k]
	if h == nil {
		h = &healthEntry{}
		r.health[k] = h
	}
	if success {
		h.consecFail = 0
		h.cooldownUntil = time.Time{}
		return
	}
	h.consecFail++
	// Exponential backoff capped at ~1 min: 2,4,8,…,64s.
	backoff := time.Duration(1<<min(h.consecFail, 6)) * time.Second
	h.cooldownUntil = now.Add(backoff)
}

// Latency returns the current EMA latency (ms) recorded for a backend, or 0
// if no observation has been recorded. Read-only, for observability and tests.
func (r *Router) Latency(k Key) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.latency[k]
}

// IsHealthy reports whether a backend is currently out of cooldown.
func (r *Router) IsHealthy(k Key) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h, ok := r.health[k]; ok {
		return time.Now().After(h.cooldownUntil)
	}
	return true
}

// weightedOrder returns backends in a weighted-random order (higher weight →
// more likely first). Backends with weight ≤ 0 are treated as weight 1.
func weightedOrder(backends []storage.RouteUpstream) []storage.RouteUpstream {
	rem := make([]storage.RouteUpstream, len(backends))
	copy(rem, backends)
	out := make([]storage.RouteUpstream, 0, len(backends))
	for len(rem) > 0 {
		total := 0
		for _, b := range rem {
			w := int(b.Weight)
			if w <= 0 {
				w = 1
			}
			total += w
		}
		pick := rand.Intn(total)
		cum := 0
		for i, b := range rem {
			w := int(b.Weight)
			if w <= 0 {
				w = 1
			}
			cum += w
			if pick < cum {
				out = append(out, b)
				rem = append(rem[:i], rem[i+1:]...)
				break
			}
		}
	}
	return out
}
