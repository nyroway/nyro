package configsync

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/nyroway/nyro/go/internal/storage"
)

// newClient builds a ConfigClient pointed at the bufconn env via dialOpts.
func newClient(cache *ConfigCache, dialOpt grpc.DialOption) *ConfigClient {
	c := NewConfigClient("passthrough:///bufnet", cache)
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

// TestConfigClient_BackoffGrows verifies the reconnect backoff schedule grows
// exponentially and is capped at maxBackoff.
func TestConfigClient_BackoffGrows(t *testing.T) {
	c := &ConfigClient{initialBackoff: 10 * time.Millisecond, maxBackoff: 100 * time.Millisecond}
	if d := c.backoff(1); d != 10*time.Millisecond {
		t.Errorf("backoff(1) = %v; want 10ms", d)
	}
	if d := c.backoff(2); d != 20*time.Millisecond {
		t.Errorf("backoff(2) = %v; want 20ms", d)
	}
	if d := c.backoff(3); d != 40*time.Millisecond {
		t.Errorf("backoff(3) = %v; want 40ms", d)
	}
	if d := c.backoff(10); d != 100*time.Millisecond {
		t.Errorf("backoff(10) = %v; want capped 100ms", d)
	}
}
