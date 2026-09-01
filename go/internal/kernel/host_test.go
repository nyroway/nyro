package kernel_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/kernel"
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

type fakeLifecycle struct {
	id           string
	events       *eventLog
	startErr     error
	closeErr     error
	startStarted chan struct{}
	startGate    chan struct{}
	closeStarted chan struct{}
	closeGate    chan struct{}
	closeDone    chan struct{}
	startCount   atomic.Int32
	closeCount   atomic.Int32
	startOnce    sync.Once
	closeOnce    sync.Once
	doneOnce     sync.Once
}

func (f *fakeLifecycle) Start(context.Context) error {
	f.startCount.Add(1)
	if f.events != nil {
		f.events.add("start:" + f.id)
	}
	if f.startStarted != nil {
		f.startOnce.Do(func() { close(f.startStarted) })
	}
	if f.startGate != nil {
		<-f.startGate
	}
	return f.startErr
}

func (f *fakeLifecycle) Close(ctx context.Context) error {
	f.closeCount.Add(1)
	if f.events != nil {
		f.events.add("close:" + f.id)
	}
	if f.closeStarted != nil {
		f.closeOnce.Do(func() { close(f.closeStarted) })
	}
	defer func() {
		if f.closeDone != nil {
			f.doneOnce.Do(func() { close(f.closeDone) })
		}
	}()
	if f.closeGate != nil {
		select {
		case <-f.closeGate:
		case <-ctx.Done():
			return errors.Join(f.closeErr, ctx.Err())
		}
	}
	return f.closeErr
}

type lateCloseLifecycle struct {
	entered    chan struct{}
	canceled   chan struct{}
	returnGate chan struct{}
	returned   chan struct{}
	err        error
}

func (*lateCloseLifecycle) Start(context.Context) error { return nil }

func (l *lateCloseLifecycle) Close(ctx context.Context) error {
	close(l.entered)
	<-ctx.Done()
	close(l.canceled)
	<-l.returnGate
	close(l.returned)
	return l.err
}

func TestHostRejectsInvalidComponentGraphsBeforeStart(t *testing.T) {
	tests := []struct {
		name       string
		components func(*fakeLifecycle) []kernel.Component
	}{
		{
			name: "empty id",
			components: func(lifecycle *fakeLifecycle) []kernel.Component {
				return []kernel.Component{{Lifecycle: lifecycle}}
			},
		},
		{
			name: "duplicate id",
			components: func(lifecycle *fakeLifecycle) []kernel.Component {
				return []kernel.Component{{ID: "a", Lifecycle: lifecycle}, {ID: "a", Lifecycle: lifecycle}}
			},
		},
		{
			name: "missing dependency",
			components: func(lifecycle *fakeLifecycle) []kernel.Component {
				return []kernel.Component{{ID: "a", After: []kernel.ComponentID{"missing"}, Lifecycle: lifecycle}}
			},
		},
		{
			name: "cycle",
			components: func(lifecycle *fakeLifecycle) []kernel.Component {
				return []kernel.Component{
					{ID: "a", After: []kernel.ComponentID{"b"}, Lifecycle: lifecycle},
					{ID: "b", After: []kernel.ComponentID{"a"}, Lifecycle: lifecycle},
				}
			},
		},
		{
			name: "nil lifecycle",
			components: func(*fakeLifecycle) []kernel.Component {
				return []kernel.Component{{ID: "a"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := &fakeLifecycle{}
			host := kernel.NewHost[string]()
			if _, err := host.Activate(context.Background(), kernel.Candidate[string]{
				Value:      "candidate",
				Components: tt.components(lifecycle),
			}); err == nil {
				t.Fatal("Activate() error = nil")
			}
			if got := lifecycle.startCount.Load(); got != 0 {
				t.Fatalf("Start() calls = %d, want 0", got)
			}
		})
	}
}

func TestHostStartsInDependencyOrderAndClosesInReverse(t *testing.T) {
	log := &eventLog{}
	closeErrB := errors.New("close b")
	closeErrD := errors.New("close d")
	component := func(id string, after []kernel.ComponentID, closeErr error) kernel.Component {
		return kernel.Component{
			ID:        kernel.ComponentID(id),
			After:     after,
			Lifecycle: &fakeLifecycle{id: id, events: log, closeErr: closeErr},
		}
	}
	host := kernel.NewHost[string]()
	activation, err := host.Activate(context.Background(), kernel.Candidate[string]{
		Version:     "v1",
		Fingerprint: "fp1",
		Value:       "runtime",
		Components: []kernel.Component{
			component("a", nil, nil),
			component("b", nil, closeErrB),
			component("c", []kernel.ComponentID{"a"}, nil),
			component("d", []kernel.ComponentID{"b"}, closeErrD),
		},
	})
	if err != nil {
		t.Fatalf("Activate(): %v", err)
	}
	if !activation.Changed || activation.Version != "v1" {
		t.Fatalf("activation = %+v", activation)
	}
	if err := host.Close(context.Background()); !errors.Is(err, closeErrB) || !errors.Is(err, closeErrD) {
		t.Fatalf("Close() error = %v, want both component errors", err)
	}

	want := []string{
		"start:a", "start:b", "start:c", "start:d",
		"close:d", "close:c", "close:b", "close:a",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestHostStartFailureRollsBackAndKeepsActive(t *testing.T) {
	host := kernel.NewHost[string]()
	old := &fakeLifecycle{id: "old"}
	if _, err := host.Activate(context.Background(), kernel.Candidate[string]{
		Version: "old", Value: "old", Components: []kernel.Component{{ID: "old", Lifecycle: old}},
	}); err != nil {
		t.Fatalf("activate old: %v", err)
	}

	log := &eventLog{}
	startErr := errors.New("start failed")
	rollbackErr := errors.New("rollback failed")
	_, err := host.Activate(context.Background(), kernel.Candidate[string]{
		Version: "bad",
		Value:   "bad",
		Components: []kernel.Component{
			{ID: "a", Lifecycle: &fakeLifecycle{id: "a", events: log}},
			{ID: "b", After: []kernel.ComponentID{"a"}, Lifecycle: &fakeLifecycle{id: "b", events: log, startErr: startErr, closeErr: rollbackErr}},
		},
	})
	if !errors.Is(err, startErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("Activate() error = %v, want start and rollback errors", err)
	}
	wantEvents := []string{"start:a", "start:b", "close:b", "close:a"}
	if got := log.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("rollback events = %v, want %v", got, wantEvents)
	}

	lease, ok := host.Acquire()
	if !ok || lease.Value() != "old" {
		t.Fatalf("Acquire() = (%v, %v), want active old generation", lease, ok)
	}
	lease.Release()
	if got := old.closeCount.Load(); got != 0 {
		t.Fatalf("old Close() calls = %d before Host.Close, want 0", got)
	}
	if closeErr := host.Close(context.Background()); closeErr != nil {
		t.Fatalf("Host.Close(): %v", closeErr)
	}
}

func TestHostActivationRetiresOldGenerationAfterLastLease(t *testing.T) {
	old := &fakeLifecycle{
		closeStarted: make(chan struct{}),
		closeGate:    make(chan struct{}),
		closeDone:    make(chan struct{}),
	}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "old", "fp-old", "old", old)
	oldLease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}
	mustActivate(t, host, "new", "fp-new", "new", &fakeLifecycle{})

	select {
	case <-old.closeStarted:
		t.Fatal("old generation closed while its lease was active")
	default:
	}
	if got := host.Status().Retiring; got != 1 {
		t.Fatalf("Status().Retiring = %d, want 1", got)
	}
	newLease, ok := host.Acquire()
	if !ok || newLease.Value() != "new" {
		t.Fatalf("Acquire() returned wrong active generation")
	}
	newLease.Release()

	oldLease.Release()
	waitClosed(t, old.closeStarted, "old generation Close did not start")
	if got := host.Status().Retiring; got != 1 {
		t.Fatalf("Status().Retiring while Close is blocked = %d, want 1", got)
	}
	close(old.closeGate)
	waitClosed(t, old.closeDone, "old generation Close did not finish")
	waitFor(t, func() bool { return host.Status().Retiring == 0 }, "old generation remained retiring")
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Host.Close(): %v", err)
	}
}

func TestLeaseReleaseIsIdempotent(t *testing.T) {
	old := &fakeLifecycle{closeDone: make(chan struct{})}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "old", "fp-old", "old", old)
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}
	mustActivate(t, host, "new", "fp-new", "new", &fakeLifecycle{})

	copied := *lease
	lease.Release()
	copied.Release()
	waitClosed(t, old.closeDone, "old generation did not close")
	if got := old.closeCount.Load(); got != 1 {
		t.Fatalf("old Close() calls = %d, want 1", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Host.Close(): %v", err)
	}
}

func TestHostCloseRejectsNewAcquireAndWaitsForLease(t *testing.T) {
	component := &fakeLifecycle{closeStarted: make(chan struct{})}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "v1", "fp1", "runtime", component)
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}

	closed := make(chan error, 1)
	go func() { closed <- host.Close(context.Background()) }()
	waitUntilAcquireRejected(t, host)
	select {
	case err := <-closed:
		t.Fatalf("Close() returned before lease release: %v", err)
	default:
	}
	select {
	case <-component.closeStarted:
		t.Fatal("component Close started before lease release")
	default:
	}

	lease.Release()
	waitClosed(t, component.closeStarted, "component Close did not start")
	if err := <-closed; err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
}

func TestHostCloseHonorsContextDeadline(t *testing.T) {
	component := &fakeLifecycle{closeStarted: make(chan struct{})}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "v1", "fp1", "runtime", component)
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	waitUntilAcquireRejected(t, host)
	select {
	case <-component.closeStarted:
		t.Fatal("component Close started before context cancellation")
	default:
	}
	cancel()
	waitClosed(t, component.closeStarted, "deadline did not force component Close")
	if err := <-closed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
	lease.Release()
	if got := component.closeCount.Load(); got != 1 {
		t.Fatalf("component Close() calls = %d, want 1", got)
	}
}

func TestHostCloseDeadlineReachesEveryCooperativeLifecycle(t *testing.T) {
	log := &eventLog{}
	host := kernel.NewHost[string]()
	if _, err := host.Activate(context.Background(), kernel.Candidate[string]{
		Value: "runtime",
		Components: []kernel.Component{
			{ID: "a", Lifecycle: &fakeLifecycle{id: "a", events: log}},
			{ID: "b", Lifecycle: &fakeLifecycle{id: "b", events: log}},
			{ID: "c", Lifecycle: &fakeLifecycle{id: "c", events: log, closeGate: make(chan struct{})}},
		},
	}); err != nil {
		t.Fatalf("Activate(): %v", err)
	}
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	waitUntilAcquireRejected(t, host)
	cancel()
	if err := <-closed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
	lease.Release()

	want := []string{"start:a", "start:b", "start:c", "close:c", "close:b", "close:a"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deadline lifecycle events = %v, want %v", got, want)
	}
}

func TestHostCloseDeadlineDoesNotWaitForBlockedActivation(t *testing.T) {
	component := &fakeLifecycle{
		startStarted: make(chan struct{}),
		startGate:    make(chan struct{}),
	}
	host := kernel.NewHost[string]()
	activated := make(chan error, 1)
	go func() {
		_, err := host.Activate(context.Background(), kernel.Candidate[string]{
			Value:      "candidate",
			Components: []kernel.Component{{ID: "component", Lifecycle: component}},
		})
		activated <- err
	}()
	waitClosed(t, component.startStarted, "component Start did not begin")

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	waitUntilAcquireRejected(t, host)
	cancel()

	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close() error = %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(component.startGate)
		<-activated
		<-closed
		t.Fatal("Close() waited for a blocked activation after its context was canceled")
	}

	close(component.startGate)
	if err := <-activated; err == nil {
		t.Fatal("Activate() published after Host.Close")
	}
	waitFor(t, func() bool { return component.closeCount.Load() == 1 }, "candidate was not rolled back")
}

func TestHostCloseStartsReadyRetirementBeforeWaitingForActivation(t *testing.T) {
	old := &fakeLifecycle{closeStarted: make(chan struct{}), closeGate: make(chan struct{})}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "old", "fp-old", "old", old)

	candidate := &fakeLifecycle{startStarted: make(chan struct{}), startGate: make(chan struct{})}
	activated := make(chan error, 1)
	go func() {
		_, err := host.Activate(context.Background(), kernel.Candidate[string]{
			Value:      "candidate",
			Components: []kernel.Component{{ID: "candidate", Lifecycle: candidate}},
		})
		activated <- err
	}()
	waitClosed(t, candidate.startStarted, "candidate Start did not begin")

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	waitUntilAcquireRejected(t, host)
	select {
	case <-old.closeStarted:
	case <-time.After(200 * time.Millisecond):
		cancel()
		close(candidate.startGate)
		<-activated
		<-closed
		t.Fatal("Close() did not begin ready generation retirement while an activation was blocked")
	}

	cancel()
	close(candidate.startGate)
	if err := <-activated; err == nil {
		t.Fatal("Activate() published after Host.Close")
	}
	if err := <-closed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
}

func TestHostCloseFreezesDeadlineResultBeforeLateRetirementError(t *testing.T) {
	lateErr := errors.New("late close failure")
	component := &lateCloseLifecycle{
		entered:    make(chan struct{}),
		canceled:   make(chan struct{}),
		returnGate: make(chan struct{}),
		returned:   make(chan struct{}),
		err:        lateErr,
	}
	host := kernel.NewHost[string]()
	mustActivate(t, host, "v1", "fp1", "runtime", component)

	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	waitClosed(t, component.entered, "component Close did not begin")
	cancel()
	waitClosed(t, component.canceled, "component Close did not observe cancellation")
	firstErr := <-closed
	if !errors.Is(firstErr, context.Canceled) || errors.Is(firstErr, lateErr) {
		t.Fatalf("first Close() error = %v, want only context cancellation", firstErr)
	}

	close(component.returnGate)
	waitClosed(t, component.returned, "component Close did not return")
	waitFor(t, func() bool { return host.Status().Retiring == 0 }, "late retirement did not finish")
	if laterErr := host.Close(context.Background()); !errors.Is(laterErr, context.Canceled) || errors.Is(laterErr, lateErr) {
		t.Fatalf("later Close() error changed from terminal result: %v", laterErr)
	}
}

func TestHostStatusReportsActiveAndRetiringGenerations(t *testing.T) {
	host := kernel.NewHost[string]()
	if got := host.Status(); got.Ready || got.ActiveVersion != "" || got.ActiveFingerprint != "" || got.Retiring != 0 {
		t.Fatalf("initial Status() = %+v", got)
	}

	old := &fakeLifecycle{closeStarted: make(chan struct{}), closeGate: make(chan struct{}), closeDone: make(chan struct{})}
	mustActivate(t, host, "v1", "fp1", "old", old)
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Acquire() rejected active generation")
	}
	mustActivate(t, host, "v2", "fp2", "new", &fakeLifecycle{})

	if got := host.Status(); !got.Ready || got.ActiveVersion != "v2" || got.ActiveFingerprint != "fp2" || got.Retiring != 1 {
		t.Fatalf("active Status() = %+v", got)
	}
	lease.Release()
	waitClosed(t, old.closeStarted, "old generation Close did not start")
	close(old.closeGate)
	waitClosed(t, old.closeDone, "old generation Close did not finish")
	waitFor(t, func() bool { return host.Status().Retiring == 0 }, "retiring count did not clear")

	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Host.Close(): %v", err)
	}
	if got := host.Status(); got.Ready || got.ActiveVersion != "" || got.ActiveFingerprint != "" {
		t.Fatalf("closed Status() = %+v", got)
	}
}

func TestHostRejectsActivationAfterClose(t *testing.T) {
	host := kernel.NewHost[string]()
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := host.Activate(context.Background(), kernel.Candidate[string]{Value: "late"}); err == nil {
		t.Fatal("Activate() after Close error = nil")
	}
}

func TestHostRejectsLateActivationWithoutWaitingForBlockedActivation(t *testing.T) {
	blocked := &fakeLifecycle{startStarted: make(chan struct{}), startGate: make(chan struct{})}
	host := kernel.NewHost[string]()
	first := make(chan error, 1)
	go func() {
		_, err := host.Activate(context.Background(), kernel.Candidate[string]{
			Value: "blocked", Components: []kernel.Component{{ID: "blocked", Lifecycle: blocked}},
		})
		first <- err
	}()
	waitClosed(t, blocked.startStarted, "first activation did not start")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := host.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}

	late := make(chan error, 1)
	go func() {
		_, err := host.Activate(context.Background(), kernel.Candidate[string]{Value: "late"})
		late <- err
	}()
	select {
	case err := <-late:
		if err == nil {
			t.Fatal("late Activate() error = nil")
		}
	case <-time.After(200 * time.Millisecond):
		close(blocked.startGate)
		<-first
		<-late
		t.Fatal("late Activate() waited behind an activation after Host.Close")
	}

	close(blocked.startGate)
	if err := <-first; err == nil {
		t.Fatal("blocked Activate() published after Host.Close")
	}
}

func TestHostCloseReturnsCompletedResultToCanceledCaller(t *testing.T) {
	host := kernel.NewHost[string]()
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if err := host.Close(ctx); err != nil {
			t.Fatalf("completed Close() returned caller context error: %v", err)
		}
	}
}

func mustActivate(t *testing.T, host *kernel.Host[string], version, fingerprint, value string, lifecycle kernel.Lifecycle) {
	t.Helper()
	components := []kernel.Component(nil)
	if lifecycle != nil {
		components = []kernel.Component{{ID: "component", Lifecycle: lifecycle}}
	}
	if _, err := host.Activate(context.Background(), kernel.Candidate[string]{
		Version: version, Fingerprint: fingerprint, Value: value, Components: components,
	}); err != nil {
		t.Fatalf("Activate(%q): %v", version, err)
	}
}

func waitUntilAcquireRejected(t *testing.T, host *kernel.Host[string]) {
	t.Helper()
	waitFor(t, func() bool {
		lease, ok := host.Acquire()
		if ok {
			lease.Release()
		}
		return !ok
	}, "Host continued accepting leases")
}

func waitClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		runtime.Gosched()
	}
}
