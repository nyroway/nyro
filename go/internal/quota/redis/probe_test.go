package redis

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProbeExercisesQuotaCapabilitiesAndCleansKeys(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	client := newClient(t, addr)
	clock := &storeClock{now: time.Unix(4_000_000, 0).Truncate(time.Minute)}
	store, err := New(client, Options{
		Now:        clock.Now,
		NewLeaseID: func() (string, error) { return "probe-id", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}

	keys := probeKeys("__nyro_probe__:probe-id", clock.Now())
	count, err := client.Exists(context.Background(), keys...).Result()
	if err != nil || count != 0 {
		t.Fatalf("probe keys remaining = %d, %v", count, err)
	}
}

func TestProbeReportsCleanupFailure(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	client := newClient(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	store, err := New(client, Options{
		NewLeaseID: func() (string, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return fmt.Sprintf("probe-id-%d", calls), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Probe(ctx)
	if err == nil || !strings.Contains(err.Error(), "quota redis: probe cleanup") {
		t.Fatalf("Probe() error = %v, want cleanup context", err)
	}
}
