package proxy

import (
	"context"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// resolvedCredential caches the provider's effective credential (API key or OAuth
// access token) and is the Go equivalent of the Rust ResolvedProviderRuntime.
type ResolvedRuntime struct {
	ProviderID string
	Credential string // the effective Bearer/API-key to use
}

// resolveProviderRuntime is the per-request credential resolver. For api-key
// providers it returns the stored key. For OAuth providers it returns the
// current access token from the config cache (or the local refresh overlay),
// refreshing it locally under a per-process mutex if expired.
//
// xDS P3b: the gateway no longer reads OAuth from storage or coordinates
// refresh via cross-replica DB CAS. Each gateway replica refreshes its own
// copy: a per-provider in-process mutex ensures only one refresh runs at a
// time, and the refreshed token is held in an in-memory overlay (checked
// before the immutable cache snapshot) until the next snapshot supersedes it.
func (g *Gateway) resolveProviderRuntime(ctx context.Context, p storage.Provider) ResolvedRuntime {
	if p.AuthMode != "oauth" {
		if p.APIKey == "" {
			return ResolvedRuntime{}
		}
		return ResolvedRuntime{ProviderID: p.ID, Credential: p.APIKey}
	}

	cred := g.oauthCredential(p.ID)
	if cred == nil {
		return ResolvedRuntime{}
	}

	// Still valid → use as-is.
	if cred.ExpiresAt != "" && !isExpired(cred.ExpiresAt) {
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	// Expired (or near-expiry) → refresh locally, one refresh per provider.
	refreshed := g.refreshOAuth(ctx, *cred)
	if refreshed.AccessToken == "" {
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}
	return ResolvedRuntime{ProviderID: p.ID, Credential: refreshed.AccessToken}
}

// oauthCredential returns the effective OAuth credential for providerID: the
// fresher of the local refresh overlay and the cache snapshot wins. Comparing
// expiry timestamps means an xDS push that carries a newer token (e.g. the
// admin reconnected the provider in the DB) supersedes a stale local refresh,
// while a local refresh that is newer than the snapshot still wins.
func (g *Gateway) oauthCredential(providerID string) *storage.OAuthCredential {
	g.oauthMu.Lock()
	ov, hasOverlay := g.oauthOverlay[providerID]
	g.oauthMu.Unlock()
	snap := g.snapshot().OAuthGet(providerID)
	if !hasOverlay {
		return snap
	}
	if snap == nil {
		c := ov
		return &c
	}
	// Both present: pick the one with the later expiry. Unparseable/empty
	// expiry is treated as the oldest, so a parseable expiry always wins.
	if expiryTime(ov.ExpiresAt).After(expiryTime(snap.ExpiresAt)) {
		c := ov
		return &c
	}
	return snap
}

// expiryTime parses an RFC3339 timestamp, returning the zero time on failure.
func expiryTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// refreshOAuth refreshes cred locally under a per-process mutex. If another
// goroutine is already refreshing this provider, it waits for that result
// rather than issuing a duplicate upstream refresh. On success the refreshed
// credential is written to the overlay and returned; on failure the original
// cred is returned unchanged.
func (g *Gateway) refreshOAuth(ctx context.Context, cred storage.OAuthCredential) storage.OAuthCredential {
	g.oauthMu.Lock()
	// Already a refresh in flight for this provider → wait for it.
	if wait, ok := g.oauthRefreshInFlight[cred.ProviderID]; ok {
		g.oauthMu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return cred
		}
		// Re-read whatever the holder produced.
		if c, ok := g.overlayGet(cred.ProviderID); ok {
			return c
		}
		return cred
	}

	// Take ownership of the refresh.
	done := make(chan struct{})
	g.oauthRefreshInFlight[cred.ProviderID] = done
	g.oauthMu.Unlock()

	refreshed := g.doRefreshOAuth(ctx, cred)
	if refreshed.AccessToken != "" {
		g.overlaySet(refreshed)
	}

	// Release the slot and wake any waiters.
	g.oauthMu.Lock()
	delete(g.oauthRefreshInFlight, cred.ProviderID)
	g.oauthMu.Unlock()
	close(done)

	if refreshed.AccessToken == "" {
		return cred
	}
	return refreshed
}

// doRefreshOAuth performs the actual driver refresh. Returns the input cred
// (empty AccessToken signals failure) when no driver/registry is available or
// the refresh errors.
func (g *Gateway) doRefreshOAuth(ctx context.Context, cred storage.OAuthCredential) storage.OAuthCredential {
	if g.driverRegistry == nil {
		return storage.OAuthCredential{}
	}
	driver, ok := g.driverRegistry.Get(cred.DriverKey)
	if !ok {
		return storage.OAuthCredential{}
	}
	refreshed, err := driver.Refresh(ctx, cred)
	if err != nil {
		return storage.OAuthCredential{}
	}
	return refreshed
}

func (g *Gateway) overlayGet(providerID string) (storage.OAuthCredential, bool) {
	g.oauthMu.Lock()
	defer g.oauthMu.Unlock()
	c, ok := g.oauthOverlay[providerID]
	return c, ok
}

func (g *Gateway) overlaySet(c storage.OAuthCredential) {
	g.oauthMu.Lock()
	g.oauthOverlay[c.ProviderID] = c
	g.oauthMu.Unlock()
}

// resolveCredential is the legacy shortcut used by callUpstream (pre-vendor).
// Delegates to resolveProviderRuntime for OAuth providers, returns the static
// API key for api-key providers.
func (g *Gateway) resolveCredential(p storage.Provider) string {
	if p.AuthMode != "oauth" {
		return p.APIKey
	}
	rt := g.resolveProviderRuntime(context.Background(), p)
	return rt.Credential
}

// isExpired returns true if the RFC3339 timestamp is in the past (or within
// 60s of expiry — proactive refresh margin).
func isExpired(expiresAt string) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(t.Add(-60 * time.Second))
}
