package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type recordingPhase struct {
	name    string
	events  *eventLog
	outcome Outcome
	apply   func(context.Context, *Exchange)
}

func (p recordingPhase) Name() string { return p.name }

func (p recordingPhase) Apply(ctx context.Context, ex *Exchange) (Outcome, Finalizer) {
	p.events.add("apply:" + p.name)
	if p.apply != nil {
		p.apply(ctx, ex)
	}
	return p.outcome, func(context.Context, *Exchange, Completion) error {
		p.events.add("finalize:" + p.name)
		return nil
	}
}

type observingPhase struct {
	recordingPhase
}

func (p observingPhase) OnDelta(context.Context, *Exchange, llm.StreamDelta) {
	p.events.add("delta:" + p.name)
}

func phaseSet(events *eventLog) PhaseSet {
	return PhaseSet{
		Observe:      recordingPhase{name: "observe", events: events},
		Resolve:      recordingPhase{name: "resolve", events: events},
		Authenticate: recordingPhase{name: "authenticate", events: events},
		Authorize:    recordingPhase{name: "authorize", events: events},
		Admit:        recordingPhase{name: "admit", events: events},
		Dispatch:     recordingPhase{name: "dispatch", events: events},
	}
}

func requireEvents(t *testing.T, events *eventLog, want []string) {
	t.Helper()
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestRunnerRunsMandatoryPhasesInFixedOrder(t *testing.T) {
	events := &eventLog{}
	phases := phaseSet(events)
	phases.PreDispatch = []Phase{
		recordingPhase{name: "pre.first", events: events},
		recordingPhase{name: "pre.second", events: events},
	}
	phases.PostResponse = []Phase{
		recordingPhase{name: "post.first", events: events},
		recordingPhase{name: "post.second", events: events},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if _, err := runner.Run(context.Background(), &Exchange{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"apply:admit",
		"apply:pre.first",
		"apply:pre.second",
		"apply:dispatch",
		"apply:post.first",
		"apply:post.second",
		"finalize:post.second",
		"finalize:post.first",
		"finalize:dispatch",
		"finalize:pre.second",
		"finalize:pre.first",
		"finalize:admit",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerRunsTerminalizerBeforeReverseFinalizers(t *testing.T) {
	events := &eventLog{}
	phases := phaseSet(events)
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	exchange := &Exchange{}
	terminalResponse := llm.NewChatResponse("terminal", "model")

	completion, err := runner.RunWithTerminalizer(context.Background(), exchange, func(_ context.Context, current *Exchange, runErr error) error {
		if runErr != nil {
			t.Fatalf("terminalizer run error = %v", runErr)
		}
		events.add("terminalize")
		current.Response = terminalResponse
		return nil
	})

	if err != nil {
		t.Fatalf("RunWithTerminalizer: %v", err)
	}
	if completion.Response != terminalResponse || completion.Error != nil {
		t.Fatalf("completion = %+v", completion)
	}
	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"apply:admit",
		"apply:dispatch",
		"terminalize",
		"finalize:dispatch",
		"finalize:admit",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerShortCircuitSkipsDispatchAndRunsFinalizers(t *testing.T) {
	events := &eventLog{}
	phases := phaseSet(events)
	phases.PreDispatch = []Phase{
		recordingPhase{name: "cache", events: events, outcome: Outcome{Decision: ShortCircuit}},
		recordingPhase{name: "never", events: events},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ex := &Exchange{Response: llm.NewChatResponse("cached", "gpt-test")}

	completion, err := runner.Run(context.Background(), ex)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if completion.Response != ex.Response {
		t.Fatal("short-circuit completion did not retain the exchange response")
	}
	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"apply:admit",
		"apply:cache",
		"finalize:cache",
		"finalize:admit",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerRejectRunsFinalizersInReverseOrder(t *testing.T) {
	events := &eventLog{}
	denied := llm.NewError(llm.ErrAuthorizationError, "denied")
	phases := phaseSet(events)
	phases.Authorize = recordingPhase{
		name:    "authorize",
		events:  events,
		outcome: Outcome{Decision: Reject, Error: denied},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	completion, err := runner.Run(context.Background(), &Exchange{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if completion.Error != denied {
		t.Fatalf("completion error = %v, want %v", completion.Error, denied)
	}
	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerRejectWithoutErrorReturnsDiagnosticAndPreservesExchangeError(t *testing.T) {
	events := &eventLog{}
	existing := llm.NewError(llm.ErrServiceUnavailable, "upstream unavailable")
	phases := phaseSet(events)
	phases.Authorize = recordingPhase{
		name:    "authorize",
		events:  events,
		outcome: Outcome{Decision: Reject},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ex := &Exchange{Error: existing}

	completion, err := runner.Run(context.Background(), ex)
	if err == nil || !strings.Contains(err.Error(), `phase "authorize" rejected without an error`) {
		t.Fatalf("Run error = %v, want malformed reject diagnostic", err)
	}
	if completion.Error != existing {
		t.Fatalf("completion error = %v, want preserved %v", completion.Error, existing)
	}
	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerCancellationRunsEveryRegisteredFinalizer(t *testing.T) {
	events := &eventLog{}
	ctx, cancel := context.WithCancel(context.Background())
	phases := phaseSet(events)
	phases.Admit = recordingPhase{
		name:   "admit",
		events: events,
		apply: func(context.Context, *Exchange) {
			cancel()
		},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if _, err := runner.Run(ctx, &Exchange{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	requireEvents(t, events, []string{
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"apply:admit",
		"finalize:admit",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerStreamObserversDoNotControlFlow(t *testing.T) {
	events := &eventLog{}
	phases := phaseSet(events)
	phases.Observe = observingPhase{recordingPhase{name: "observe", events: events}}
	phases.PreDispatch = []Phase{
		observingPhase{recordingPhase{name: "stream.audit", events: events}},
	}
	runner, err := NewRunner(phases)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ex := &Exchange{}

	runner.ObserveDelta(context.Background(), ex, &llm.TextDelta{Text: "hello"})
	if _, err := runner.Run(context.Background(), ex); err != nil {
		t.Fatalf("Run: %v", err)
	}

	requireEvents(t, events, []string{
		"delta:observe",
		"delta:stream.audit",
		"apply:observe",
		"apply:resolve",
		"apply:authenticate",
		"apply:authorize",
		"apply:admit",
		"apply:stream.audit",
		"apply:dispatch",
		"finalize:dispatch",
		"finalize:stream.audit",
		"finalize:admit",
		"finalize:authorize",
		"finalize:authenticate",
		"finalize:resolve",
		"finalize:observe",
	})
}

func TestRunnerRejectsDuplicateOrReservedExtensionPhaseNames(t *testing.T) {
	tests := []struct {
		name string
		pre  []Phase
		post []Phase
	}{
		{
			name: "empty",
			pre:  []Phase{recordingPhase{name: " ", events: &eventLog{}}},
		},
		{
			name: "duplicate across slots",
			pre:  []Phase{recordingPhase{name: "audit", events: &eventLog{}}},
			post: []Phase{recordingPhase{name: "audit", events: &eventLog{}}},
		},
		{
			name: "reserved mandatory phase",
			pre:  []Phase{recordingPhase{name: "Observe", events: &eventLog{}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phases := phaseSet(&eventLog{})
			phases.PreDispatch = tt.pre
			phases.PostResponse = tt.post
			if _, err := NewRunner(phases); err == nil {
				t.Fatal("NewRunner returned nil error")
			}
		})
	}
}
