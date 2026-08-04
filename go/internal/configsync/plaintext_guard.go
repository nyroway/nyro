package configsync

import (
	"fmt"
	"net"
	"strings"
)

// The config-sync stream carries every upstream's credentials_json. There is no
// way to trim that — a data plane cannot call an upstream without its
// credentials — so encryption is the only protection, and an unauthenticated
// plaintext config-sync port is equivalent to publishing the entire
// configuration to anyone who can reach it.
//
// The guards below therefore fail closed, but only where it costs nothing: both
// --listen and --sync-listen default to loopback, so the single-machine
// zero-config path never trips them. The trigger is deliberately "does this
// channel leave the host", not "is TLS configured" — a loopback channel needs
// neither TLS nor a token, and demanding them there would be noise that trains
// operators to ignore the real warnings.
//
// There is intentionally NO --insecure-style override. The only combination
// these guards reject is non-loopback + no TLS + no token, and the escape hatch
// for it is to set a token — which is itself a security improvement, unlike a
// flag whose entire function is to disable a safety check. A flag would also
// need its own rules about what it does and does not relax, which is exactly
// the kind of ambiguity that turns into a real hole.

// GuardPlaintextListen rejects binding the config-sync server to a
// non-loopback address with neither transport encryption nor authentication.
//
// This is the side that actually leaks: an open, unauthenticated port serves
// the full configuration to whoever connects, no matter how carefully the
// proxies are configured.
func GuardPlaintextListen(addr string, tlsConfigured, tokenConfigured bool) error {
	if tlsConfigured || tokenConfigured || isLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf(`config-sync is bound to a non-loopback address (%s) with neither transport
encryption nor authentication. The stream carries upstream API credentials in
the clear to any client that connects.

Fix by either:
  - set --sync-token (or NYRO_SERVE_SYNC_TOKEN) to require a join credential, or
  - configure mTLS with --sync-tls-ca/-cert/-key (see `+"`nyro tool ca`"+`)
Or bind --sync-listen to a loopback address`, addr)
}

// GuardPlaintextDial rejects subscribing to a non-loopback config-sync endpoint
// with neither transport encryption nor authentication.
//
// Symmetric with GuardPlaintextListen on purpose. The listener is where the
// exposure is created, but a proxy dialling in the clear still pulls every
// upstream credential across the network where it can be read or tampered
// with, so accepting it silently on this side would undercut the other guard.
func GuardPlaintextDial(target string, tlsConfigured, tokenConfigured bool) error {
	if tlsConfigured || tokenConfigured || isLoopbackAddr(target) {
		return nil
	}
	return fmt.Errorf(`--server points at a non-loopback address (%s) with neither transport
encryption nor authentication. The config-sync stream delivers upstream API
credentials in the clear over this network path.

Fix by either:
  - set --sync-token (or NYRO_PROXY_SYNC_TOKEN) to match the server's join credential, or
  - configure mTLS with --sync-tls-ca/-cert/-key (see `+"`nyro tool ca`"+`)
Or point --server at a loopback address`, target)
}

// isLoopbackAddr reports whether a host:port address refers to this host only.
//
// A bare hostname that is not "localhost" counts as non-loopback even if it
// happens to resolve to 127.0.0.1: resolution can change under us, and guessing
// generously is the wrong direction for a fail-closed check. An address without
// a parseable host:port shape is also treated as non-loopback — if we cannot
// tell, we do not wave it through.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackListenAddress reports whether a listen address is loopback-only.
// Exported for the commands' own non-config-sync checks (e.g. warning about an
// unauthenticated management API on a public address).
func IsLoopbackListenAddress(addr string) bool { return isLoopbackAddr(addr) }
