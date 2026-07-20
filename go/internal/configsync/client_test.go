package configsync

import (
	"context"
	mrand "math/rand"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nyroway/nyro/go/internal/storage"
)

// newClient builds a ConfigClient pointed at the bufconn env via dialOpts.
func newClient(cache *ConfigCache, dialOpt grpc.DialOption) *ConfigClient {
	c := NewConfigClient("passthrough:///bufnet", cache, "19530", nil)
	c.initialBackoff = 20 * time.Millisecond
	c.maxBackoff = 100 * time.Millisecond
	c.dialOpts = []grpc.DialOption{
		dialOpt,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	return c
}

func TestConfigClient_ReceivesAndSwaps(t *testing.T) {
	st, _, rOpen, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	cache := &ConfigCache{}
	c := newClient(cache, dialOpt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// initial snapshot should land in the cache.
	waitFor(t, 2*time.Second, func() bool {
		return cache.Ready() && cache.Load().RouteByModel(rOpen.Model) != nil
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
		return cache.Load().RouteByModel("client-model") != nil
	})
}

// TestConfigClient_StopsOnContextCancel verifies Run returns when the context
// is cancelled (clean shutdown is the client's other contract besides receive).
func TestConfigClient_StopsOnContextCancel(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	_, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	cache := &ConfigCache{}
	c := newClient(cache, dialOpt)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()

	waitFor(t, 2*time.Second, func() bool { return cache.Ready() })
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
