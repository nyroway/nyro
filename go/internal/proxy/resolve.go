package proxy

import (
	"context"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// resolvedCredential caches the provider's effective credential (API key or OAuth
// access token) and is the Go equivalent of the Rust ResolvedProviderRuntime.
// When an OAuth token is expired, the resolve path refreshes it via CAS-locked
// try_begin_refresh → driver.Refresh → complete_refresh.
type ResolvedRuntime struct {
	ProviderID string
	Credential string // the effective Bearer/API-key to use
}

// resolveProviderRuntime is the per-request credential resolver. For api-key
// providers it returns the stored key. For OAuth providers it returns the
// stored access token, refreshing it via CAS lock if expired.
//
// This is the hot path equivalent of the Rust
// admin/oauth.rs::resolve_provider_runtime.
func (g *Gateway) resolveProviderRuntime(ctx context.Context, p storage.Provider) ResolvedRuntime {
	if p.AuthMode != "oauth" {
		if p.APIKey == "" {
			return ResolvedRuntime{}
		}
		return ResolvedRuntime{ProviderID: p.ID, Credential: p.APIKey}
	}

	cred, _ := g.Storage.OAuthCredentials().Get(p.ID)
	if cred == nil {
		return ResolvedRuntime{}
	}

	// Check expiry.
	if cred.ExpiresAt != "" && !isExpired(cred.ExpiresAt) {
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	// Token expired (or near-expiry) → CAS-locked refresh.
	// Phase 1: try_begin_refresh (optimistic lock on status_version).
	locked, _ := g.Storage.OAuthCredentials().TryBeginRefresh(p.ID, cred.StatusVersion)
	if locked == nil {
		// Another goroutine/replica is already refreshing — use whatever we have.
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	// Phase 2: refresh via driver (if registered).
	// The driver registry is set up by the server startup via SetDriverRegistry.
	if g.driverRegistry == nil {
		_ = g.Storage.OAuthCredentials().FailRefresh(p.ID, "no driver registry configured")
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	driver, ok := g.driverRegistry.Get(cred.DriverKey)
	if !ok {
		_ = g.Storage.OAuthCredentials().FailRefresh(p.ID, "unknown driver: "+cred.DriverKey)
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	refreshed, err := driver.Refresh(ctx, *cred)
	if err != nil {
		_ = g.Storage.OAuthCredentials().FailRefresh(p.ID, err.Error())
		return ResolvedRuntime{ProviderID: p.ID, Credential: cred.AccessToken}
	}

	// Phase 3: complete_refresh (update stored token).
	_, _ = g.Storage.OAuthCredentials().CompleteRefresh(p.ID, storage.UpsertOAuthCredential{
		DriverKey:    refreshed.DriverKey,
		Scheme:       refreshed.Scheme,
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		ExpiresAt:    refreshed.ExpiresAt,
	})

	return ResolvedRuntime{ProviderID: p.ID, Credential: refreshed.AccessToken}
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
