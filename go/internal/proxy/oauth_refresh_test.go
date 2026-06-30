package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

// fakeDriver is a minimal AuthDriver whose Refresh produces a deterministic new
// access token and counts how many refreshes ran (to assert the per-process
// mutex collapses concurrent refreshes into one).
type fakeDriver struct {
	key   string
	calls *atomic.Int64
	delay time.Duration // optional, to widen the race window
}

func (f *fakeDriver) Name() string            { return f.key }
func (f *fakeDriver) Scheme() auth.AuthScheme { return auth.SchemeOAuthAuthCode }
func (f *fakeDriver) Start(context.Context, string) (auth.StartResult, error) {
	return auth.StartResult{}, nil
}
func (f *fakeDriver) Exchange(context.Context, string, string, string, string) (storage.OAuthCredential, error) {
	return storage.OAuthCredential{}, nil
}
func (f *fakeDriver) Refresh(ctx context.Context, cred storage.OAuthCredential) (storage.OAuthCredential, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return cred, ctx.Err()
		}
	}
	cred.AccessToken = cred.AccessToken + "-refreshed"
	cred.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return cred, nil
}

// newGatewayWithOAuth builds a Gateway wired with a config cache holding one
// (expired) OAuth credential for provider "prov" and a fake driver registered
// under the cred's DriverKey.
func newGatewayWithOAuth(t *testing.T, calls *atomic.Int64) (*Gateway, *fakeDriver) {
	t.Helper()
	cache := &xds.ConfigCache{}
	builder := &xds.Snapshot{}
	builder.SetProvider(storage.Provider{ID: "prov", AuthMode: "oauth"})
	builder.SetOAuth(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "old",
		ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), // expired
	})
	cache.Swap(builder.Done())
	g := NewGatewayWithCache(cache)
	drv := &fakeDriver{key: "drv", calls: calls}
	reg := auth.NewRegistry()
	reg.Register("drv", drv)
	g.SetDriverRegistry(reg)
	return g, drv
}

func TestResolveProviderRuntime_OAuthRefreshesOnceUnderConcurrency(t *testing.T) {
	var calls atomic.Int64
	g, drv := newGatewayWithOAuth(t, &calls)
	drv.delay = 20 * time.Millisecond // hold the refresh open so concurrent callers pile up

	p := storage.Provider{ID: "prov", AuthMode: "oauth"}

	const N = 8
	var wg sync.WaitGroup
	results := make([]string, N)
	start := make(chan struct{})
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			rt := g.resolveProviderRuntime(context.Background(), p)
			results[i] = rt.Credential
		}()
	}
	close(start)
	wg.Wait()

	// Exactly one upstream refresh ran (the per-process mutex collapsed the rest).
	if got := calls.Load(); got != 1 {
		t.Errorf("refresh calls = %d; want 1 (mutex should serialize)", got)
	}
	// Every caller got the refreshed token.
	for i, r := range results {
		if r != "old-refreshed" {
			t.Errorf("result[%d] = %q; want %q", i, r, "old-refreshed")
		}
	}
}

func TestResolveProviderRuntime_OAuthValidTokenNoRefresh(t *testing.T) {
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	// Replace the cached cred with a still-valid one.
	b := &xds.Snapshot{}
	b.SetOAuth(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "good",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	g.Cache.Swap(b.Done())

	rt := g.resolveProviderRuntime(context.Background(), storage.Provider{ID: "prov", AuthMode: "oauth"})
	if rt.Credential != "good" {
		t.Errorf("credential = %q; want %q", rt.Credential, "good")
	}
	if calls.Load() != 0 {
		t.Errorf("refresh ran %d times; want 0 for valid token", calls.Load())
	}
}

func TestResolveProviderRuntime_OverlayWinsOverSnapshot(t *testing.T) {
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	// Snapshot token is "old" + expired; overlay holds a fresh valid token.
	g.overlaySet(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "overlay-fresh",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})

	rt := g.resolveProviderRuntime(context.Background(), storage.Provider{ID: "prov", AuthMode: "oauth"})
	if rt.Credential != "overlay-fresh" {
		t.Errorf("credential = %q; want overlay-fresh", rt.Credential)
	}
	if calls.Load() != 0 {
		t.Errorf("refresh ran %d times; want 0 (overlay is valid)", calls.Load())
	}
}

func TestResolveProviderRuntime_NewerSnapshotSupersedesOverlay(t *testing.T) {
	// If an xDS push carries a token fresher than the overlay (e.g. the admin
	// reconnected the provider in the DB), the snapshot must win — otherwise the
	// gateway would keep serving a stale local refresh.
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	// Overlay: fresh but older than the snapshot.
	g.overlaySet(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "overlay-older",
		ExpiresAt: time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
	})
	b := &xds.Snapshot{}
	b.SetOAuth(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "snap-fresher",
		ExpiresAt: time.Now().Add(50 * time.Minute).UTC().Format(time.RFC3339),
	})
	g.Cache.Swap(b.Done())

	rt := g.resolveProviderRuntime(context.Background(), storage.Provider{ID: "prov", AuthMode: "oauth"})
	if rt.Credential != "snap-fresher" {
		t.Errorf("credential = %q; want snap-fresher (newer snapshot should win)", rt.Credential)
	}
	if calls.Load() != 0 {
		t.Errorf("refresh ran %d times; want 0 (both tokens valid)", calls.Load())
	}
}

func TestResolveProviderRuntime_ApiKeyProvider(t *testing.T) {
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	rt := g.resolveProviderRuntime(context.Background(), storage.Provider{ID: "prov", AuthMode: "apikey", APIKey: "sk-1"})
	if rt.Credential != "sk-1" {
		t.Errorf("apikey credential = %q; want sk-1", rt.Credential)
	}
	if calls.Load() != 0 {
		t.Errorf("refresh ran on apikey provider: %d", calls.Load())
	}
}

func TestProactiveRefresh_SkipsOverlayWhenFresh(t *testing.T) {
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	// Overlay already fresh → proactive refresh must be a no-op.
	g.overlaySet(storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "overlay-fresh",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	g.proactiveRefresh(context.Background(), storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "old",
		ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if calls.Load() != 0 {
		t.Errorf("refresh ran %d times; want 0 (overlay fresh)", calls.Load())
	}
}

func TestProactiveRefresh_RefreshesExpired(t *testing.T) {
	var calls atomic.Int64
	g, _ := newGatewayWithOAuth(t, &calls)
	g.proactiveRefresh(context.Background(), storage.OAuthCredential{
		ProviderID: "prov", DriverKey: "drv", AccessToken: "old",
		ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if calls.Load() != 1 {
		t.Errorf("refresh ran %d times; want 1", calls.Load())
	}
	if c, ok := g.overlayGet("prov"); !ok || c.AccessToken != "old-refreshed" {
		t.Errorf("overlay = %+v; want old-refreshed", c)
	}
}
