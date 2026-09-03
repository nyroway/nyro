package configsync

import (
	"context"
	"errors"
	mrand "math/rand"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/storage"
)

// newClient builds a ConfigClient pointed at the bufconn env via dialOpts.
func newClient(applier SnapshotApplier, dialOpt grpc.DialOption) *ConfigClient {
	c := NewConfigClient("passthrough:///bufnet", applier, "19530", nil)
	c.initialBackoff = 20 * time.Millisecond
	c.maxBackoff = 100 * time.Millisecond
	c.dialOpts = []grpc.DialOption{
		dialOpt,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	return c
}

type recordingApplier struct {
	mu        sync.Mutex
	snapshot  *configsnapshot.Snapshot
	versions  []string
	rejectFor int
	applied   chan struct{}
	attempted chan struct{}
}

type cacheSnapshotApplier struct{ cache *configsnapshot.Cache }

func (a cacheSnapshotApplier) Apply(_ context.Context, snapshot *configsnapshot.Snapshot, _ string) error {
	a.cache.Swap(snapshot)
	return nil
}

func (a *recordingApplier) Apply(_ context.Context, snapshot *configsnapshot.Snapshot, version string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.versions = append(a.versions, version)
	if a.attempted != nil {
		select {
		case a.attempted <- struct{}{}:
		default:
		}
	}
	if a.rejectFor > 0 {
		a.rejectFor--
		return errors.New("candidate rejected")
	}
	a.snapshot = snapshot
	if a.applied != nil {
		select {
		case a.applied <- struct{}{}:
		default:
		}
	}
	return nil
}

func (a *recordingApplier) load() *configsnapshot.Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshot
}

func TestConfigClient_ReceivesAndSwaps(t *testing.T) {
	st, _, rOpen, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	applier := &recordingApplier{}
	c := newClient(applier, dialOpt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// initial snapshot should land in the cache.
	waitFor(t, 2*time.Second, func() bool {
		snapshot := applier.load()
		return snapshot != nil && snapshot.RouteByModel(rOpen.Model) != nil
	})

	// Bump epoch + add a route, then Notify; cache should reflect the push.
	if _, err := st.Storage().Routes().Create(storage.CreateRoute{Model: "client-model"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Storage().Settings().Set("config_epoch", "5"); err != nil {
		t.Fatal(err)
	}
	srv.Notify()
	waitFor(t, 2*time.Second, func() bool {
		snapshot := applier.load()
		return snapshot != nil && snapshot.RouteByModel("client-model") != nil
	})
}

func TestConfigClientRejectedCandidateKeepsReceiving(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	applier := &recordingApplier{rejectFor: 1, applied: make(chan struct{}, 1), attempted: make(chan struct{}, 1)}
	c := newClient(applier, dialOpt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()

	select {
	case <-applier.attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("ConfigClient did not submit the initial candidate")
	}
	if _, err := st.Storage().Routes().Create(storage.CreateRoute{Model: "accepted-after-reject"}); err != nil {
		t.Fatal(err)
	}
	srv.Notify()
	select {
	case <-applier.applied:
	case <-done:
		t.Fatal("ConfigClient stopped after SnapshotApplier rejected a candidate")
	case <-time.After(2 * time.Second):
		t.Fatal("ConfigClient did not apply a later candidate after rejection")
	}
	if snapshot := applier.load(); snapshot == nil || snapshot.RouteByModel("accepted-after-reject") == nil {
		t.Fatal("later candidate was not applied")
	}
}

// TestConfigClient_StopsOnContextCancel verifies Run returns when the context
// is cancelled (clean shutdown is the client's other contract besides receive).
func TestConfigClient_StopsOnContextCancel(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	_, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	applier := &recordingApplier{}
	c := newClient(applier, dialOpt)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()

	waitFor(t, 2*time.Second, func() bool { return applier.load() != nil })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// TestConfigClient_BaseBackoffGrows verifies the deterministic backoff schedule
// grows exponentially and is capped at maxBackoff.
func TestConfigClient_BaseBackoffGrows(t *testing.T) {
	c := &ConfigClient{initialBackoff: 10 * time.Millisecond, maxBackoff: 100 * time.Millisecond}
	if d := c.baseBackoff(1); d != 10*time.Millisecond {
		t.Errorf("baseBackoff(1) = %v; want 10ms", d)
	}
	if d := c.baseBackoff(2); d != 20*time.Millisecond {
		t.Errorf("baseBackoff(2) = %v; want 20ms", d)
	}
	if d := c.baseBackoff(3); d != 40*time.Millisecond {
		t.Errorf("baseBackoff(3) = %v; want 40ms", d)
	}
	if d := c.baseBackoff(10); d != 100*time.Millisecond {
		t.Errorf("baseBackoff(10) = %v; want capped 100ms", d)
	}
}

// TestConfigClient_BackoffJitter verifies backoff applies equal jitter: the
// result stays in [base/2, base) and varies across calls (not lockstep).
func TestConfigClient_BackoffJitter(t *testing.T) {
	c := &ConfigClient{
		initialBackoff: 10 * time.Millisecond,
		maxBackoff:     100 * time.Millisecond,
		rng:            mrand.New(mrand.NewSource(1)),
	}
	base := c.baseBackoff(4) // 80ms, below the cap
	seen := map[time.Duration]struct{}{}
	for range 50 {
		d := c.backoff(4)
		if d < base/2 || d >= base {
			t.Fatalf("backoff(4) = %v; want in [%v, %v)", d, base/2, base)
		}
		seen[d] = struct{}{}
	}
	if len(seen) < 2 {
		t.Errorf("jitter produced no variation across 50 calls: %v", seen)
	}
}
