package configsync

import (
	"strings"
	"testing"
)

// The guard's whole design rests on the trigger being "does this channel leave
// the host", not "is TLS configured" — that is what keeps the single-machine
// zero-config path silent while still failing closed off-host.
func TestGuardPlaintextListen(t *testing.T) {
	tests := []struct {
		name      string
		addr      string
		tls       bool
		token     bool
		wantError bool
	}{
		{name: "loopback plaintext unauthenticated is the default and must pass", addr: "127.0.0.1:19532"},
		{name: "loopback ipv6", addr: "[::1]:19532"},
		{name: "localhost by name", addr: "localhost:19532"},
		{name: "loopback with token", addr: "127.0.0.1:19532", token: true},

		{name: "all interfaces, no protection", addr: "0.0.0.0:19532", wantError: true},
		{name: "ipv6 any, no protection", addr: "[::]:19532", wantError: true},
		{name: "routable address, no protection", addr: "10.0.0.10:19532", wantError: true},
		{name: "port-only binds every interface", addr: ":19532", wantError: true},

		{name: "routable rescued by a token", addr: "10.0.0.10:19532", token: true},
		{name: "routable rescued by mTLS", addr: "10.0.0.10:19532", tls: true},

		// A hostname could resolve anywhere, and resolution can change after
		// the check; a fail-closed guard must not guess generously.
		{name: "unparseable address is not waved through", addr: "not-an-address", wantError: true},
		{name: "hostname is treated as off-host", addr: "server.internal:19532", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GuardPlaintextListen(tt.addr, tt.tls, tt.token)
			if tt.wantError && err == nil {
				t.Fatalf("GuardPlaintextListen(%q, tls=%v, token=%v) = nil; want an error", tt.addr, tt.tls, tt.token)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("GuardPlaintextListen(%q, tls=%v, token=%v) = %v; want nil", tt.addr, tt.tls, tt.token, err)
			}
		})
	}
}

// The rejection message replaces the discoverability a flag would have
// provided, so it must name every way out rather than just stating the refusal.
func TestGuardPlaintextListen_ErrorNamesEveryWayOut(t *testing.T) {
	err := GuardPlaintextListen("0.0.0.0:19532", false, false)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"--sync-token", "NYRO_SERVE_SYNC_TOKEN", "--sync-tls-ca", "nyro tool ca", "loopback", "0.0.0.0:19532"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message is missing %q:\n%s", want, err)
		}
	}
}

func TestGuardPlaintextDial(t *testing.T) {
	if err := GuardPlaintextDial("127.0.0.1:19532", false, false); err != nil {
		t.Errorf("loopback dial must pass unauthenticated: %v", err)
	}
	if err := GuardPlaintextDial("10.0.0.10:19532", false, false); err == nil {
		t.Error("off-host plaintext dial with no token must be refused, symmetrically with the listen side")
	}
	if err := GuardPlaintextDial("10.0.0.10:19532", false, true); err != nil {
		t.Errorf("a token must rescue an off-host dial: %v", err)
	}
	if err := GuardPlaintextDial("10.0.0.10:19532", true, false); err != nil {
		t.Errorf("mTLS must rescue an off-host dial: %v", err)
	}
}
