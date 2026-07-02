package plugin

import (
	"context"
	"sync"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// Phase is a fixed request-lifecycle phase — a closed set. New behaviour is
// added by hooking within a phase, never by inventing a new phase.
type Phase string

const (
	PhaseOnRequest  Phase = "on_request"  // before routing
	PhaseOnAccess   Phase = "on_access"   // after authn/authz
	PhaseOnUpstream Phase = "on_upstream" // per-attempt, before the HTTP call
	PhaseOnResponse Phase = "on_response" // after decode → IR
	PhaseOnLog      Phase = "on_log"      // terminal, read-only
)

// PhaseContext bundles the per-request state a hook may touch.
type PhaseContext struct {
	Ctx      context.Context
	Request  *ir.AiRequest
	Response *ir.AiResponse // set for non-stream OnResponse; nil for stream
	Delta    ir.StreamDelta // set for per-delta stream OnResponse
	Bag      *ContextBag
}

// ContextBag is a typed extension map shared across phases of a single request.
// Hooks store state here to pass it to later phases (e.g. metrics counters).
type ContextBag struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewContextBag creates an empty bag.
func NewContextBag() *ContextBag { return &ContextBag{data: map[string]any{}} }

// Set stores a value under key.
func (b *ContextBag) Set(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = value
}

// Get retrieves a value by key. Returns nil, false if not found.
func (b *ContextBag) Get(key string) (any, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.data[key]
	return v, ok
}

// PhaseOutcome is the result of running a hook.
type PhaseOutcome int

const (
	OutcomeContinue     PhaseOutcome = iota
	OutcomeShortCircuit              // hook short-circuited with a direct response
	OutcomeReject                    // hook rejected the request
)

// PhaseHook is a lifecycle hook anchored to exactly one Phase.
type PhaseHook interface {
	Name() string
	Phase() Phase
	Run(ctx *PhaseContext) PhaseOutcome
}

var hooks []PhaseHook

// Register adds a lifecycle hook. Intended for package init().
func Register(h PhaseHook) { hooks = append(hooks, h) }

// RunPhaseHooks runs every hook anchored to the given phase, in registration
// order, stopping at the first non-Continue outcome. With no hooks registered
// (the P1 default) this is a no-op returning Continue.
func RunPhaseHooks(phase Phase, pctx *PhaseContext) PhaseOutcome {
	for _, h := range hooks {
		if h.Phase() != phase {
			continue
		}
		if out := h.Run(pctx); out != OutcomeContinue {
			return out
		}
	}
	return OutcomeContinue
}
