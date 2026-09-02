package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

// PhaseSet explicitly supplies every mandatory phase and the only two slots
// in which optional phases may run.
type PhaseSet struct {
	Observe      Phase
	Resolve      Phase
	Authenticate Phase
	Authorize    Phase
	Admit        Phase
	PreDispatch  []Phase
	Dispatch     Phase
	PostResponse []Phase
}

// Runner executes one immutable PhaseSet in the LLM runtime's fixed order.
type Runner struct {
	phases    []Phase
	observers []StreamObserver
}

var reservedPhaseNames = map[string]struct{}{
	"observe":      {},
	"resolve":      {},
	"authenticate": {},
	"authorize":    {},
	"admit":        {},
	"predispatch":  {},
	"dispatch":     {},
	"postresponse": {},
	"finalize":     {},
}

// NewRunner validates phases and fixes their execution order for all runs.
func NewRunner(set PhaseSet) (*Runner, error) {
	mandatory := []struct {
		position string
		phase    Phase
	}{
		{position: "Observe", phase: set.Observe},
		{position: "Resolve", phase: set.Resolve},
		{position: "Authenticate", phase: set.Authenticate},
		{position: "Authorize", phase: set.Authorize},
		{position: "Admit", phase: set.Admit},
		{position: "Dispatch", phase: set.Dispatch},
	}
	for _, entry := range mandatory {
		if entry.phase == nil {
			return nil, fmt.Errorf("pipeline: %s phase is required", entry.position)
		}
	}
	if err := validateExtensions(set.PreDispatch, set.PostResponse); err != nil {
		return nil, err
	}

	phases := make([]Phase, 0, len(mandatory)+len(set.PreDispatch)+len(set.PostResponse))
	phases = append(phases,
		set.Observe,
		set.Resolve,
		set.Authenticate,
		set.Authorize,
		set.Admit,
	)
	phases = append(phases, set.PreDispatch...)
	phases = append(phases, set.Dispatch)
	phases = append(phases, set.PostResponse...)

	runner := &Runner{phases: phases}
	for _, phase := range phases {
		if observer, ok := phase.(StreamObserver); ok {
			runner.observers = append(runner.observers, observer)
		}
	}
	return runner, nil
}

func validateExtensions(slots ...[]Phase) error {
	seen := make(map[string]struct{})
	for _, phases := range slots {
		for _, phase := range phases {
			if phase == nil {
				return errors.New("pipeline: extension phase is nil")
			}
			name := strings.TrimSpace(phase.Name())
			if name == "" {
				return errors.New("pipeline: extension phase name is required")
			}
			key := normalizedPhaseName(name)
			if _, reserved := reservedPhaseNames[key]; reserved {
				return fmt.Errorf("pipeline: extension phase name %q is reserved", name)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("pipeline: duplicate extension phase name %q", name)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func normalizedPhaseName(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_':
			return -1
		default:
			return r
		}
	}, strings.ToLower(strings.TrimSpace(name)))
}

// Run applies phases until termination or cancellation, then invokes every
// registered Finalizer in reverse order. Finalizer failures are joined so one
// failure cannot prevent later cleanup.
func (r *Runner) Run(ctx context.Context, ex *Exchange) (Completion, error) {
	if r == nil {
		return Completion{}, errors.New("pipeline: Runner is nil")
	}
	if ex == nil {
		return Completion{}, errors.New("pipeline: Exchange is nil")
	}

	finalizers := make([]Finalizer, 0, len(r.phases))
	var runErr error
	for _, phase := range r.phases {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		outcome, finalizer := phase.Apply(ctx, ex)
		if finalizer != nil {
			finalizers = append(finalizers, finalizer)
		}
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		switch outcome.Decision {
		case Continue:
		case ShortCircuit:
			if outcome.Error != nil {
				ex.Error = outcome.Error
			}
			goto finalize
		case Reject:
			ex.Error = outcome.Error
			goto finalize
		default:
			runErr = fmt.Errorf("pipeline: phase %q returned invalid decision %d", phase.Name(), outcome.Decision)
			goto finalize
		}
	}

finalize:
	completion := completionFrom(ex)
	for i := len(finalizers) - 1; i >= 0; i-- {
		if err := finalizers[i](ctx, ex, completion); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return completion, runErr
}

// ObserveDelta delivers a stream event to observers in phase order. Observers
// have no result channel and therefore cannot reject or rewrite stream flow.
func (r *Runner) ObserveDelta(ctx context.Context, ex *Exchange, delta llm.StreamDelta) {
	for _, observer := range r.observers {
		observer.OnDelta(ctx, ex, delta)
	}
}

func completionFrom(ex *Exchange) Completion {
	return Completion{
		Response: ex.Response,
		Error:    ex.Error,
		Usage:    ex.Usage,
		Streamed: ex.Streamed,
	}
}
