package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// StartOAuthRefreshLoop launches a background goroutine that proactively
// refreshes OAuth credentials expiring within 5 minutes. Runs every 2 minutes.
//
// xDS P3b: refresh is now local. The loop reads credentials from the config
// cache snapshot and refreshes each expiring one under the same per-process
// mutex as the request path (resolveProviderRuntime). There is no cross-replica
// CAS and no storage read: each gateway replica refreshes its own in-memory
// copy and holds the refreshed token in the overlay until the next snapshot.
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
	for _, cred := range g.snapshot().OAuthList() {
		if cred.ExpiresAt == "" {
			continue
		}
		if !isExpiredIn(cred.ExpiresAt, 5*time.Minute) {
			continue
		}
		g.proactiveRefresh(ctx, cred)
	}
}

func (g *Gateway) proactiveRefresh(ctx context.Context, cred storage.OAuthCredential) {
	// The overlay may already hold a fresher token than the snapshot; if it is
	// still valid, skip the refresh entirely.
	if c, ok := g.overlayGet(cred.ProviderID); ok && c.ExpiresAt != "" && !isExpired(c.ExpiresAt) {
		return
	}
	refreshed := g.refreshOAuth(ctx, cred)
	if refreshed.AccessToken == "" {
		slog.Warn("proactive OAuth refresh failed",
			"provider", cred.ProviderID, "driver", cred.DriverKey)
		return
	}
	slog.Info("proactively refreshed OAuth credential",
		"provider", cred.ProviderID, "driver", cred.DriverKey)
}

// isExpiredIn reports whether expiresAt is within horizon of now (past or
// future). Unparseable timestamps are treated as not-expiring (safe default:
// avoids churn from malformed data).
func isExpiredIn(expiresAt string, horizon time.Duration) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(t.Add(-horizon))
}
