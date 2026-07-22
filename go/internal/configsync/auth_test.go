package configsync

import (
	"context"
	"net"
	"testing"
	"time"
)

// startTokenServer starts a token-guarded config-sync server on a free port.
func startTokenServer(t *testing.T, ctx context.Context, tokens []string) string {
	t.Helper()
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv := NewConfigServer(st.Storage())
	addr := freeAddr(t)
	shutdown, err := ServeGRPC(ctx, addr, srv, nil, StreamTokenAuth(tokens))
	if err != nil {
		t.Fatalf("ServeGRPC: %v", err)
	}
	t.Cleanup(shutdown)
	return addr
}

// subscribes reports whether a client presenting token receives a snapshot
// within a short window.
func subscribes(t *testing.T, addr, token string) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := &ConfigCache{}
	c := NewConfigClient(addr, cache, "19530", nil)
	c.initialBackoff = 10 * time.Millisecond
	c.maxBackoff = 20 * time.Millisecond
	c.SetJoinToken(token)
	go func() { _ = c.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if cache.Ready() {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestStreamTokenAuth_RejectsMissingAndWrongTokens(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := startTokenServer(t, ctx, []string{"correct-horse"})

	if subscribes(t, addr, "correct-horse") != true {
		t.Error("a client presenting the configured token must receive a snapshot")
	}
	if subscribes(t, addr, "") {
		t.Error("a client presenting no token must not receive a snapshot")
	}
	if subscribes(t, addr, "battery-staple") {
		t.Error("a client presenting a wrong token must not receive a snapshot")
	}
}

// Rotation is the reason tokens are a set rather than a single value: the
// server accepts old and new together for the window in which proxies are
// rolled onto the new one.
func TestStreamTokenAuth_AcceptsAnyConfiguredTokenForRotation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := startTokenServer(t, ctx, []string{"old-token", "new-token"})

	if !subscribes(t, addr, "old-token") {
		t.Error("the outgoing token must still be accepted during rotation")
	}
	if !subscribes(t, addr, "new-token") {
		t.Error("the incoming token must be accepted during rotation")
	}
	if subscribes(t, addr, "retired-token") {
		t.Error("a token that was never configured must be rejected")
	}
}

// An empty token list means "no token auth configured": the interceptor is not
// installed at all, which is what keeps the loopback zero-config path working.
func TestStreamTokenAuth_NoTokensMeansNoInterceptor(t *testing.T) {
	if opt := StreamTokenAuth(nil); opt != nil {
		t.Error("StreamTokenAuth(nil) must return a nil ServerOption, not an interceptor that rejects everything")
	}
	if opt := StreamTokenAuth([]string{}); opt != nil {
		t.Error("StreamTokenAuth([]) must return a nil ServerOption")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := startTokenServer(t, ctx, nil)
	if !subscribes(t, addr, "") {
		t.Error("with no tokens configured an unauthenticated client must still subscribe")
	}
}

// freeAddr reserves a loopback port and releases it, so ServeGRPC can bind a
// known address (it does not report the port it resolved).
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestTokenAccepted_ConstantTimeSetMembership(t *testing.T) {
	accepted := [][]byte{[]byte("alpha"), []byte("beta")}
	for _, tok := range []string{"alpha", "beta"} {
		if !tokenAccepted(accepted, tok) {
			t.Errorf("tokenAccepted(%q) = false; want true", tok)
		}
	}
	for _, tok := range []string{"", "alph", "alphaa", "gamma"} {
		if tokenAccepted(accepted, tok) {
			t.Errorf("tokenAccepted(%q) = true; want false", tok)
		}
	}
}
