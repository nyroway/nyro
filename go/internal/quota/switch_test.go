package quota

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestUnavailableSwitchFailsClosed(t *testing.T) {
	ctx := context.Background()
	sw := NewUnavailableSwitch()
	if sw.Ready() {
		t.Fatal("unavailable Switch reports ready")
	}
	if _, err := sw.Value(ctx, "k", "requests", time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Value() error = %v, want ErrUnavailable", err)
	}
	if err := sw.Record(ctx, "k", Usage{Requests: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Record() error = %v, want ErrUnavailable", err)
	}
	if _, _, err := sw.Acquire(ctx, "k", 1, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Acquire() error = %v, want ErrUnavailable", err)
	}
}

func TestSwitchTracksHealthByGeneration(t *testing.T) {
	backend := &switchFakeStore{value: 7}
	sw := NewSwitch(backend)
	if !sw.Ready() {
		t.Fatal("Switch with backend is not ready")
	}

	backend.valueErr = errors.New("redis unavailable")
	if _, err := sw.Value(context.Background(), "k", "requests", time.Minute); err == nil {
		t.Fatal("Value() error = nil")
	}
	if sw.Ready() {
		t.Fatal("operation error did not mark Switch unhealthy")
	}

	next := &switchFakeStore{value: 11}
	generation := sw.Swap(next, nil, time.Second)
	if !sw.Ready() {
		t.Fatal("Swap() did not restore readiness")
	}
	sw.MarkHealthy(generation-1, false)
	if !sw.Ready() {
		t.Fatal("stale generation changed current health")
	}
	sw.MarkHealthy(generation, false)
	if sw.Ready() {
		t.Fatal("current generation was not marked unhealthy")
	}
	sw.MarkHealthy(generation, true)
	if !sw.Ready() {
		t.Fatal("current generation was not restored")
	}
	got, err := sw.Value(context.Background(), "k", "requests", time.Minute)
	if err != nil || got != 11 {
		t.Fatalf("Value() after Swap = %d, %v; want 11, nil", got, err)
	}
}

func TestSwitchCanceledOperationDoesNotPoisonHealth(t *testing.T) {
	backend := &switchFakeStore{valueErr: context.Canceled}
	sw := NewSwitch(backend)
	if _, err := sw.Value(context.Background(), "k", "requests", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("Value() error = %v, want context.Canceled", err)
	}
	if !sw.Ready() {
		t.Fatal("caller cancellation poisoned backend health")
	}
}

func TestSwitchAdmitRequestPreservesHealthOnDenialAndContention(t *testing.T) {
	backend := &switchFakeStore{admitDenied: true}
	sw := NewSwitch(backend)
	allowed, err := sw.AdmitRequest(context.Background(), "consumer", []RequestLimit{{Limit: 1, Window: time.Minute}})
	if err != nil || allowed {
		t.Fatalf("denial = %v, %v", allowed, err)
	}
	if !sw.Ready() {
		t.Fatal("clean denial poisoned health")
	}

	backend.mu.Lock()
	backend.admitDenied = false
	backend.admitErr = ErrAdmissionContended
	backend.mu.Unlock()
	_, err = sw.AdmitRequest(context.Background(), "consumer", []RequestLimit{{Limit: 1, Window: time.Minute}})
	if !errors.Is(err, ErrAdmissionContended) {
		t.Fatalf("error = %v, want ErrAdmissionContended", err)
	}
	if !sw.Ready() {
		t.Fatal("contention poisoned health")
	}
}

func TestSwitchAdmitRequestBackendFailureMarksUnhealthy(t *testing.T) {
	backend := &switchFakeStore{admitErr: errors.New("redis unavailable")}
	sw := NewSwitch(backend)
	_, _ = sw.AdmitRequest(context.Background(), "consumer", []RequestLimit{{Limit: 1, Window: time.Minute}})
	if sw.Ready() {
		t.Fatal("backend failure kept Switch healthy")
	}
}

func TestSwitchRetiresBackendAfterLeaseRelease(t *testing.T) {
	backend := &switchFakeStore{}
	sw := NewSwitch(backend)
	lease, allowed, err := sw.Acquire(context.Background(), "k", 1, time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("Acquire() = %v, %v, %v", lease, allowed, err)
	}

	retired := make(chan struct{})
	sw.Swap(&switchFakeStore{}, func() { close(retired) }, time.Second)
	select {
	case <-retired:
		t.Fatal("backend retired while lease was active")
	default:
	}

	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("backend was not retired after lease release")
	}
}

func TestSwitchForceRetiresAbandonedLease(t *testing.T) {
	sw := NewSwitch(&switchFakeStore{})
	lease, allowed, err := sw.Acquire(context.Background(), "k", 1, time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("Acquire() = %v, %v, %v", lease, allowed, err)
	}

	retired := make(chan struct{})
	sw.Swap(&switchFakeStore{}, func() { close(retired) }, 20*time.Millisecond)
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("force timer did not retire backend")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchLeaseReleaseErrorMarksOriginalGenerationOnly(t *testing.T) {
	backend := &switchFakeStore{leaseErr: errors.New("release failed")}
	sw := NewSwitch(backend)
	lease, allowed, err := sw.Acquire(context.Background(), "k", 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("Acquire() = %v, %v, %v", lease, allowed, err)
	}
	current := &switchFakeStore{}
	sw.Swap(current, nil, time.Second)

	if err := lease.Release(context.Background()); err == nil {
		t.Fatal("Release() error = nil")
	}
	if !sw.Ready() {
		t.Fatal("old lease release poisoned current generation")
	}
}

func TestSwitchShutdownFailsClosed(t *testing.T) {
	sw := NewSwitch(NewMemory())
	sw.Shutdown()
	if sw.Ready() {
		t.Fatal("Shutdown Switch reports ready")
	}
	if err := sw.Record(context.Background(), "k", Usage{Requests: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Record() error = %v, want ErrUnavailable", err)
	}
}

type switchFakeStore struct {
	mu          sync.Mutex
	value       int64
	valueErr    error
	recordErr   error
	admitErr    error
	admitDenied bool
	acquireErr  error
	leaseErr    error
	records     int
}

func (f *switchFakeStore) AdmitRequest(context.Context, string, []RequestLimit) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.admitErr != nil {
		return false, f.admitErr
	}
	return !f.admitDenied, nil
}

func (f *switchFakeStore) Value(context.Context, string, string, time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, f.valueErr
}

func (f *switchFakeStore) Record(context.Context, string, Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records++
	return f.recordErr
}

func (f *switchFakeStore) Acquire(context.Context, string, int64, time.Duration) (Lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return nil, false, f.acquireErr
	}
	return &switchFakeLease{err: f.leaseErr}, true, nil
}

type switchFakeLease struct {
	once sync.Once
	err  error
}

func (l *switchFakeLease) Release(context.Context) error {
	var err error
	l.once.Do(func() { err = l.err })
	return err
}
