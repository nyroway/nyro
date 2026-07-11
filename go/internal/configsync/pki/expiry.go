package pki

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"
)

// ExpiryWarningWindow is how far ahead of a leaf certificate's expiry
// admin/gateway should start logging a renewal warning. Certificates here
// are offline-signed and manually rotated (see nyro ca sign-*); nothing
// renews them automatically, so operators need advance notice before the
// config-sync channel silently stops working.
const ExpiryWarningWindow = 30 * 24 * time.Hour

// LeafNotAfter returns the NotAfter time of a loaded TLS certificate's leaf
// (the first certificate in the chain, i.e. the one LoadServerTLS/
// LoadClientTLS presented for this node — not the CA).
func LeafNotAfter(cert tls.Certificate) (time.Time, error) {
	if len(cert.Certificate) == 0 {
		return time.Time{}, fmt.Errorf("pki: tls.Certificate has no leaf")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("pki: parse leaf certificate: %w", err)
	}
	return leaf.NotAfter, nil
}

// ExpiringSoon reports whether notAfter is within ExpiryWarningWindow of now
// (or already past).
func ExpiringSoon(notAfter, now time.Time) bool {
	return notAfter.Sub(now) <= ExpiryWarningWindow
}

// WatchExpiry checks every leaf certificate in cfg immediately, then again
// every interval until ctx is cancelled, invoking warn(notAfter) for each one
// that's within ExpiryWarningWindow of expiring. These certificates are
// offline-signed and manually rotated (see nyro ca sign-*) — nothing renews
// them — so this is the only mechanism that gives an operator advance notice
// before the config-sync channel silently stops working. A nil cfg (Tier 0,
// --config-insecure) is a no-op: there's nothing to expire.
//
// warn is called synchronously from the goroutine WatchExpiry starts;
// callers needing more than a log line should keep it fast and non-blocking.
func WatchExpiry(ctx context.Context, cfg *tls.Config, interval time.Duration, warn func(notAfter time.Time)) {
	if cfg == nil {
		return
	}
	check := func() {
		now := time.Now()
		for _, cert := range cfg.Certificates {
			notAfter, err := LeafNotAfter(cert)
			if err != nil {
				continue
			}
			if ExpiringSoon(notAfter, now) {
				warn(notAfter)
			}
		}
	}
	check()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}
