package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// StartOAuthRefreshLoop launches a background goroutine that proactively
// refreshes OAuth credentials expiring within 5 minutes and recovers stuck
// "refreshing" states older than 60 seconds. Runs every 2 minutes.
// Ported from admin/oauth.rs::refresh_oauth_providers.
func (g *Gateway) StartOAuthRefreshLoop(ctx context.Context) {
	if g.driverRegistry == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.refreshExpiringCredentials(ctx)
			}
		}
	}()
}

func (g *Gateway) refreshExpiringCredentials(ctx context.Context) {
	// Step 1: recover stuck refreshing states.
	if recovered, err := g.Storage.OAuthCredentials().RecoverStaleRefreshing(60 * time.Second); err == nil && recovered > 0 {
		slog.Info("recovered stale OAuth refresh states", "count", recovered)
	}

	// Step 2: proactively refresh tokens expiring within 5 minutes.
	expiring, err := g.Storage.OAuthCredentials().ListExpiring(5 * time.Minute)
	if err != nil {
		return
	}
	for _, cred := range expiring {
		g.proactiveRefresh(ctx, cred)
	}
}

func (g *Gateway) proactiveRefresh(ctx context.Context, cred storage.OAuthCredential) {
	driver, ok := g.driverRegistry.Get(cred.DriverKey)
	if !ok {
		return
	}

	// CAS lock — skip if another goroutine is already refreshing this provider.
	locked, _ := g.Storage.OAuthCredentials().TryBeginRefresh(cred.ProviderID, cred.StatusVersion)
	if locked == nil {
		return // already being refreshed
	}

	refreshed, err := driver.Refresh(ctx, cred)
	if err != nil {
		slog.Warn("proactive OAuth refresh failed",
			"provider", cred.ProviderID, "driver", cred.DriverKey, "error", err)
		_ = g.Storage.OAuthCredentials().FailRefresh(cred.ProviderID, err.Error())
		return
	}

	_, _ = g.Storage.OAuthCredentials().CompleteRefresh(cred.ProviderID, storage.UpsertOAuthCredential{
		DriverKey:    refreshed.DriverKey,
		Scheme:       refreshed.Scheme,
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		ExpiresAt:    refreshed.ExpiresAt,
	})
	slog.Info("proactively refreshed OAuth credential",
		"provider", cred.ProviderID, "driver", cred.DriverKey)
}
