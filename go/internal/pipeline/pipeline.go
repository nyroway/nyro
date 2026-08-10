// Package pipeline is the request-lifecycle Stage chain.
//
// A request runs through an ordered list of Stages. Each Stage receives the
// Exchange and a next func; what it does around that call is the whole model:
//
//	func (s example) Handle(ex *Exchange, next func() error) error {
//	    // ... inbound work (inspect or mutate ex.Req) ...
//	    if err := next(); err != nil { return err }
//	    // ... outbound work (read ex.Resp / ex.Usage / ex.Status) ...
//	    return nil
//	}
//
// Returning without calling next short-circuits: every later Stage and the
// terminal handler are skipped, and the Stages already on the stack still run
// their outbound half. That is how a cache hit or a rejected request ends the
// exchange without a special-cased outcome enum.
//
// Layer: 0 (foundation) — may import internal/protocol/llm only. Stages
// themselves live with the subsystem they belong to (observability owns its
// Stage, proxy owns authn/authz/quota), so this package holds the contract and
// nothing else.
package pipeline

import (
	"context"
	"net/http"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/llm/ir"
)

// Stage is one step in the chain. Handle runs once per request.
//
// Implementations must call next at most once. Not calling it short-circuits
// the rest of the chain (see the package comment); calling it twice is a bug
// the chain does not defend against.
type Stage interface {
	// Name identifies the Stage in errors and traces.
	Name() string
	// Handle wraps the rest of the chain. Return next's error unchanged
	// unless the Stage is deliberately absorbing it.
	Handle(ex *Exchange, next func() error) error
}

// StreamStage is an optional capability a Stage may also implement to observe
// a streaming response frame by frame, in the spirit of http.Flusher: the name
// describes what the type can do, not a pattern it participates in.
//
// OnDelta has no next and no error return on purpose. By the time frames flow,
// Handle has already returned, the status code and headers are on the wire,
// and the body is streaming to the client — there is no longer any way to
// reject the exchange, only to watch it. Stages that do not care about
// individual frames (authn, authz, quota, cache) simply omit the method and
// are never called per frame.
type StreamStage interface {
	Stage
	OnDelta(ex *Exchange, d ir.StreamDelta)
}

// Exchange is the per-request state threaded through the chain: one
// request-response round trip, in the sense the term carries in gateways. It
// is deliberately not called a Context — it carries a real context.Context in
// Ctx, and conflating the two would make every Stage signature ambiguous.
//
// Fields are populated as they become known: Route and Upstream are zero until
// routing and backend selection succeed, Usage and Status until the upstream
// responds. A Stage reading a field the exchange never reached (an early
// rejection, say) sees the zero value, which is what a terminal Stage wants
// when it reports a request that failed before it had a backend.
//
// Exchange is not safe for concurrent use. It is owned by one request; the
// chain runs its Stages sequentially, and OnDelta is called from the same
// goroutine that drives the stream.
type Exchange struct {
	// Ctx is the request context. Stages that start spans replace it with
	// the derived context so later Stages inherit the parent.
	Ctx context.Context

	// W and R are the raw HTTP pair. A Stage that short-circuits writes its
	// own response through W.
	W http.ResponseWriter
	R *http.Request

	// Req is the decoded request in canonical IR form, set before the chain
	// runs. Stages may mutate it inbound (injecting tools, rewriting the
	// model) and every later Stage sees the change.
	Req *ir.AiRequest

	// Resp is the decoded non-streaming response, nil for streaming
	// exchanges and for any request that never reached an upstream.
	Resp *ir.AiResponse

	// Usage accumulates token counts: set once from the parsed response for
	// a non-streaming exchange, updated per frame for a streaming one.
	Usage ir.Usage

	// Status is the status code sent to the client.
	Status int

	// Started is when the exchange began, for latency.
	Started time.Time

	// Stream reports whether the client asked for a streaming response.
	Stream bool

	// ConsumerID identifies the authenticated consumer, empty for an open
	// route. Set by the authn Stage.
	ConsumerID string

	// Ext carries values between Stages that have no business being a named
	// field — a Stage's own bookkeeping, or state shared by a pair of
	// cooperating Stages. Prefer a field for anything the core flow reads.
	// Nil until first use; use SetExt/GetExt rather than touching it.
	Ext map[string]any
}

// SetExt stores a value under key, allocating the map on first use.
func (ex *Exchange) SetExt(key string, value any) {
	if ex.Ext == nil {
		ex.Ext = make(map[string]any, 4)
	}
	ex.Ext[key] = value
}

// GetExt returns the value stored under key, or nil if absent.
func (ex *Exchange) GetExt(key string) any {
	if ex.Ext == nil {
		return nil
	}
	return ex.Ext[key]
}

// Chain is an ordered, immutable list of Stages. Build one at startup with
// NewChain and reuse it for every request; it holds no per-request state.
type Chain struct {
	stages []Stage
	// streamStages is the subset implementing StreamStage, resolved once at
	// construction so the per-frame path costs no type assertions.
	streamStages []StreamStage
}

// NewChain returns a Chain running stages in the given order. The slice is
// copied, so later mutation by the caller cannot affect the chain.
func NewChain(stages ...Stage) *Chain {
	c := &Chain{stages: append([]Stage(nil), stages...)}
	for _, s := range c.stages {
		if ss, ok := s.(StreamStage); ok {
			c.streamStages = append(c.streamStages, ss)
		}
	}
	return c
}

// Stages returns the chain's stages in order. The result is a copy.
func (c *Chain) Stages() []Stage {
	return append([]Stage(nil), c.stages...)
}

// Run executes the chain and then terminal, which performs the actual upstream
// call. Stages run in order on the way in and unwind in reverse on the way out,
// so a Stage's post-next code always sees the work of every Stage after it.
//
// A nil Chain runs terminal directly, which keeps tests that construct a
// gateway without a chain working.
func (c *Chain) Run(ex *Exchange, terminal func() error) error {
	if c == nil || len(c.stages) == 0 {
		return terminal()
	}
	var run func(i int) error
	run = func(i int) error {
		if i == len(c.stages) {
			return terminal()
		}
		return c.stages[i].Handle(ex, func() error { return run(i + 1) })
	}
	return run(0)
}

// EmitDelta forwards one streaming frame to every Stage that implements
// StreamStage, in chain order. Called once per frame on the streaming path.
func (c *Chain) EmitDelta(ex *Exchange, d ir.StreamDelta) {
	if c == nil {
		return
	}
	for _, s := range c.streamStages {
		s.OnDelta(ex, d)
	}
}
