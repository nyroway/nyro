package plugin

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

type countingHook struct {
	name  string
	phase Phase
	count *int32
}

func (h *countingHook) Name() string { return h.name }
func (h *countingHook) Phase() Phase { return h.phase }
func (h *countingHook) Run(_ *PhaseContext) PhaseOutcome {
	atomic.AddInt32(h.count, 1)
	return OutcomeContinue
}

func TestRunPhaseHooksInvokesRegisteredHook(t *testing.T) {
	var n int32
	h := &countingHook{name: "test-on-request", phase: PhaseOnRequest, count: &n}
	Register(h)
	defer func() {
		for i, hh := range hooks {
			if hh == h {
				hooks = append(hooks[:i], hooks[i+1:]...)
				break
			}
		}
	}()

	RunPhaseHooks(PhaseOnRequest, &PhaseContext{Ctx: context.Background(), Request: ir.NewAiRequest("m", nil)})
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Errorf("OnRequest hook ran %d times, want 1", got)
	}
	// A hook anchored to OnRequest must not fire for OnAccess.
	RunPhaseHooks(PhaseOnAccess, &PhaseContext{Ctx: context.Background()})
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Errorf("hook fired on wrong phase: %d", got)
	}
	if len(hooks) < 1 {
		t.Error("hooks should contain the registered hook")
	}
}
