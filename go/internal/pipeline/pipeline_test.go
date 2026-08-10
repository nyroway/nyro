package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/llm/ir"
)

// recorder is a Stage that appends to a shared trace on the way in and on the
// way out, so a test can assert the exact interleaving.
type recorder struct {
	name  string
	trace *[]string
	// skipNext makes the Stage short-circuit instead of continuing.
	skipNext bool
	// err, when non-nil, is returned instead of calling next.
	err error
}

func (r recorder) Name() string { return r.name }

func (r recorder) Handle(ex *Exchange, next func() error) error {
	*r.trace = append(*r.trace, r.name+":in")
	if r.err != nil {
		*r.trace = append(*r.trace, r.name+":err")
		return r.err
	}
	if r.skipNext {
		*r.trace = append(*r.trace, r.name+":short")
		return nil
	}
	err := next()
	*r.trace = append(*r.trace, r.name+":out")
	return err
}

// streamRecorder additionally implements StreamStage.
type streamRecorder struct {
	recorder
	deltas *int
}

func (s streamRecorder) OnDelta(ex *Exchange, d ir.StreamDelta) { *s.deltas++ }

func TestRunOrderUnwindsInReverse(t *testing.T) {
	var got []string
	c := NewChain(
		recorder{name: "a", trace: &got},
		recorder{name: "b", trace: &got},
	)
	err := c.Run(&Exchange{}, func() error {
		got = append(got, "terminal")
		return nil
	})
	if err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	want := "a:in b:in terminal b:out a:out"
	if strings.Join(got, " ") != want {
		t.Errorf("trace = %q, want %q", strings.Join(got, " "), want)
	}
}

// TestShortCircuitSkipsTerminalButUnwinds is the core of the model: a Stage
// that does not call next ends the exchange, yet the Stages already on the
// stack still get their outbound half — which is what lets a terminal
// telemetry Stage report a request rejected by auth.
func TestShortCircuitSkipsTerminalButUnwinds(t *testing.T) {
	var got []string
	terminalRan := false
	c := NewChain(
		recorder{name: "outer", trace: &got},
		recorder{name: "blocker", trace: &got, skipNext: true},
		recorder{name: "never", trace: &got},
	)
	if err := c.Run(&Exchange{}, func() error { terminalRan = true; return nil }); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if terminalRan {
		t.Error("terminal ran despite the short circuit")
	}
	want := "outer:in blocker:in blocker:short outer:out"
	if strings.Join(got, " ") != want {
		t.Errorf("trace = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestErrorPropagatesAndStillUnwinds(t *testing.T) {
	var got []string
	sentinel := errors.New("denied")
	c := NewChain(
		recorder{name: "outer", trace: &got},
		recorder{name: "failing", trace: &got, err: sentinel},
	)
	err := c.Run(&Exchange{}, func() error { return nil })
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run returned %v, want %v", err, sentinel)
	}
	want := "outer:in failing:in failing:err outer:out"
	if strings.Join(got, " ") != want {
		t.Errorf("trace = %q, want %q", strings.Join(got, " "), want)
	}
}

// TestTerminalErrorReachesCaller pins that an upstream failure surfaces
// through the chain rather than being swallowed by it.
func TestTerminalErrorReachesCaller(t *testing.T) {
	var got []string
	sentinel := errors.New("upstream exploded")
	c := NewChain(recorder{name: "a", trace: &got})
	if err := c.Run(&Exchange{}, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Run returned %v, want %v", err, sentinel)
	}
	if strings.Join(got, " ") != "a:in a:out" {
		t.Errorf("trace = %q, want the stage to still unwind", strings.Join(got, " "))
	}
}

func TestNilAndEmptyChainRunTerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    *Chain
	}{
		{"nil", nil},
		{"empty", NewChain()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			if err := tc.c.Run(&Exchange{}, func() error { ran = true; return nil }); err != nil {
				t.Fatalf("Run returned %v, want nil", err)
			}
			if !ran {
				t.Error("terminal did not run")
			}
		})
	}
}

// TestEmitDeltaOnlyReachesStreamStages pins the reason StreamStage is a
// separate, optional interface: a plain Stage must not be woken per frame.
func TestEmitDeltaOnlyReachesStreamStages(t *testing.T) {
	var got []string
	deltas := 0
	c := NewChain(
		recorder{name: "plain", trace: &got},
		streamRecorder{recorder: recorder{name: "streaming", trace: &got}, deltas: &deltas},
	)
	ex := &Exchange{}
	c.EmitDelta(ex, &ir.UsageDelta{})
	c.EmitDelta(ex, &ir.UsageDelta{})

	if deltas != 2 {
		t.Errorf("stream stage saw %d deltas, want 2", deltas)
	}
	if len(got) != 0 {
		t.Errorf("EmitDelta ran Handle on a stage: %v", got)
	}
}

func TestEmitDeltaOnNilChainIsNoop(t *testing.T) {
	var c *Chain
	c.EmitDelta(&Exchange{}, &ir.UsageDelta{}) // must not panic
}

func TestNewChainCopiesStages(t *testing.T) {
	var got []string
	stages := []Stage{recorder{name: "a", trace: &got}}
	c := NewChain(stages...)
	stages[0] = recorder{name: "swapped", trace: &got}

	if err := c.Run(&Exchange{}, func() error { return nil }); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if strings.Join(got, " ") != "a:in a:out" {
		t.Errorf("trace = %q — chain used the caller's mutated slice", strings.Join(got, " "))
	}
}

func TestExtRoundTrips(t *testing.T) {
	ex := &Exchange{}
	if got := ex.GetExt("missing"); got != nil {
		t.Errorf("GetExt on empty Exchange = %v, want nil", got)
	}
	ex.SetExt("k", 42)
	if got := ex.GetExt("k"); got != 42 {
		t.Errorf("GetExt = %v, want 42", got)
	}
}
