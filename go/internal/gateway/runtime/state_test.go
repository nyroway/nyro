package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	platformstate "github.com/nyroway/nyro/go/internal/platform/state"
	"github.com/nyroway/nyro/go/internal/quota"
)

func TestStateManagerAbsentStateInstallsMemory(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	var factoryCalls atomic.Int64
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			factoryCalls.Add(1)
			return stateBackend{}, errors.New("factory must not handle memory")
		},
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.Apply((&configsnapshot.Builder{}).Build())
	if !quotaSwitch.Ready() {
		t.Fatal("absent State did not install Memory")
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory calls = %d, want 0", factoryCalls.Load())
	}
}

func TestStateManagerStrictRedisFailureStaysUnavailableAndRedactsURL(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			return stateBackend{}, errors.New("dial failed with secret")
		},
		connectTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	err := manager.ApplyStrict(context.Background(), stateSnapshot("redis://alice:secret@127.0.0.1:1/0"))
	if err == nil {
		t.Fatal("ApplyStrict() error = nil")
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("ApplyStrict() error is not redacted: %q", err)
	}
	if quotaSwitch.Ready() {
		t.Fatal("strict failure substituted a ready backend")
	}
}

func TestStateManagerRetriesFirstRedisFailure(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	var attempts atomic.Int64
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			if attempts.Add(1) == 1 {
				return stateBackend{}, errors.New("temporarily unavailable")
			}
			return stateBackend{store: quota.NewMemory()}, nil
		},
		connectTimeout: 50 * time.Millisecond,
		retryDelay:     func(int) time.Duration { return time.Millisecond },
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.Apply(stateSnapshot("redis://127.0.0.1:6379/0"))
	waitState(t, time.Second, quotaSwitch.Ready)
	if attempts.Load() < 2 {
		t.Fatalf("factory attempts = %d, want at least 2", attempts.Load())
	}
}

func TestStateManagerFailedHotUpdateKeepsPreviousBackend(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	attempted := make(chan struct{}, 1)
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			select {
			case attempted <- struct{}{}:
			default:
			}
			return stateBackend{}, errors.New("unreachable")
		},
		connectTimeout: 50 * time.Millisecond,
		retryDelay:     func(int) time.Duration { return time.Hour },
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.Apply((&configsnapshot.Builder{}).Build())
	limits := []quota.RequestLimit{{Limit: 1, Window: time.Minute}}
	if allowed, err := quotaSwitch.AdmitRequest(context.Background(), "consumer", limits); err != nil || !allowed {
		t.Fatalf("initial admission = %v, %v", allowed, err)
	}
	manager.Apply(stateSnapshot("redis://127.0.0.1:1/0"))
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("hot-update factory was not called")
	}
	if !quotaSwitch.Ready() {
		t.Fatal("failed hot update discarded previous backend")
	}
	allowed, err := quotaSwitch.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || allowed {
		t.Fatalf("previous backend admission = %v, %v; want shared denial", allowed, err)
	}
}

func TestStateManagerNewDesiredConfigCancelsOldCandidate(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	oldStarted := make(chan struct{})
	oldCanceled := make(chan struct{})
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(ctx context.Context, cfg platformstate.Config) (stateBackend, error) {
			if strings.Contains(cfg.URL, "old") {
				close(oldStarted)
				<-ctx.Done()
				close(oldCanceled)
				return stateBackend{}, ctx.Err()
			}
			return stateBackend{store: quota.NewMemory()}, nil
		},
		connectTimeout: time.Second,
		retryDelay:     func(int) time.Duration { return time.Millisecond },
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.Apply(stateSnapshot("redis://old.example:6379/0"))
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old candidate did not start")
	}
	manager.Apply(stateSnapshot("redis://new.example:6379/0"))
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("old candidate was not canceled")
	}
	waitState(t, time.Second, quotaSwitch.Ready)
}

func TestStateManagerInvalidAndIdenticalConfigsDoNotChurn(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	var calls atomic.Int64
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			calls.Add(1)
			return stateBackend{store: quota.NewMemory()}, nil
		},
		connectTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	redisSnapshot := stateSnapshot("redis://127.0.0.1:6379/0")
	manager.Apply(redisSnapshot)
	waitState(t, time.Second, quotaSwitch.Ready)
	manager.Apply(redisSnapshot)
	if calls.Load() != 1 {
		t.Fatalf("identical config factory calls = %d, want 1", calls.Load())
	}

	var invalid configsnapshot.Builder
	invalid.SetSetting(platformstate.SettingTypeKey, "etcd")
	manager.Apply(invalid.Build())
	if !quotaSwitch.Ready() {
		t.Fatal("invalid config discarded current backend")
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid config factory calls = %d, want 1", calls.Load())
	}
}

func TestStateManagerHealthFailureAndRecovery(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	var healthy atomic.Bool
	healthy.Store(true)
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			return stateBackend{
				store: quota.NewMemory(),
				ping: func(context.Context) error {
					if healthy.Load() {
						return nil
					}
					return errors.New("ping failed")
				},
			}, nil
		},
		connectTimeout: 50 * time.Millisecond,
		healthInterval: 2 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.Apply(stateSnapshot("redis://127.0.0.1:6379/0"))
	waitState(t, time.Second, quotaSwitch.Ready)
	healthy.Store(false)
	waitState(t, time.Second, func() bool { return !quotaSwitch.Ready() })
	healthy.Store(true)
	waitState(t, time.Second, quotaSwitch.Ready)
}

func TestStateManagerRetiresRedisAfterLeaseReleaseAndOnShutdown(t *testing.T) {
	quotaSwitch := quota.NewUnavailableSwitch()
	retired := make(chan struct{}, 1)
	var retireCalls atomic.Int64
	manager := newStateManager(context.Background(), quotaSwitch, stateManagerOptions{
		factory: func(context.Context, platformstate.Config) (stateBackend, error) {
			return stateBackend{
				store: quota.NewMemory(),
				retire: func() {
					retireCalls.Add(1)
					retired <- struct{}{}
				},
			}, nil
		},
		connectTimeout: 50 * time.Millisecond,
		retireAfter:    time.Second,
	})

	manager.Apply(stateSnapshot("redis://127.0.0.1:6379/0"))
	waitState(t, time.Second, quotaSwitch.Ready)
	lease, allowed, err := quotaSwitch.Acquire(context.Background(), "consumer", 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("Acquire() = %v, %v, %v", lease, allowed, err)
	}
	manager.Apply((&configsnapshot.Builder{}).Build())
	select {
	case <-retired:
		t.Fatal("Redis retired while Lease was active")
	default:
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("Redis was not retired after Lease release")
	}
	if retireCalls.Load() != 1 {
		t.Fatalf("retire calls = %d, want 1", retireCalls.Load())
	}

	manager.Apply(stateSnapshot("redis://127.0.0.1:6379/1"))
	waitState(t, time.Second, quotaSwitch.Ready)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if retireCalls.Load() != 2 {
		t.Fatalf("retire calls after Shutdown = %d, want 2", retireCalls.Load())
	}
	if quotaSwitch.Ready() {
		t.Fatal("Switch ready after Shutdown")
	}
}

func stateSnapshot(rawURL string) *configsnapshot.Snapshot {
	var builder configsnapshot.Builder
	builder.SetSetting(platformstate.SettingTypeKey, string(platformstate.KindRedis))
	builder.SetSetting(platformstate.SettingURLKey, rawURL)
	return builder.Build()
}

func waitState(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
