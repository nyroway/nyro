package configsync

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authMetadataKey / bearerPrefix mirror the management API's Bearer convention
// so operators meet one credential-passing shape across the whole product.
const (
	authMetadataKey = "authorization"
	bearerPrefix    = "Bearer "
)

// StreamTokenAuth returns a stream interceptor that requires every subscriber
// to present one of tokens as `authorization: Bearer <token>`.
//
// This is a JOIN credential, not an identity. Every holder of a valid token is
// equally authorized and indistinguishable from every other; the node_id in the
// node list stays self-reported and therefore spoofable. Per-node identity
// requires mTLS, where StreamConfig derives it from the verified client
// certificate's SPIFFE SAN instead.
//
// Tokens accepted as a set rather than a single value so they can be rotated
// without downtime: add the new token to the server, roll the proxies onto it,
// then drop the old one.
//
// An empty token list returns nil, meaning "no token auth configured" — the
// caller decides whether that is acceptable for the transport in use (see
// GuardPlaintextListen).
func StreamTokenAuth(tokens []string) grpc.ServerOption {
	if len(tokens) == 0 {
		return nil
	}
	accepted := make([][]byte, 0, len(tokens))
	for _, t := range tokens {
		accepted = append(accepted, []byte(t))
	}
	return grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		presented, err := bearerFromContext(ss.Context())
		if err != nil {
			return err
		}
		if !tokenAccepted(accepted, presented) {
			return status.Error(codes.Unauthenticated, "config-sync: invalid join token")
		}
		return handler(srv, ss)
	})
}

// bearerFromContext extracts the bearer value from the incoming metadata.
func bearerFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "config-sync: missing join token (set --sync-token on the proxy)")
	}
	for _, v := range md.Get(authMetadataKey) {
		if after, found := strings.CutPrefix(v, bearerPrefix); found {
			return after, nil
		}
	}
	return "", status.Error(codes.Unauthenticated, "config-sync: missing join token (set --sync-token on the proxy)")
}

// tokenAccepted reports whether presented matches any accepted token.
//
// Every candidate is compared even after a match is found: returning early
// would make the response time depend on the token's position in the list, and
// each comparison itself is constant-time so it does not leak how much of a
// token was correct.
func tokenAccepted(accepted [][]byte, presented string) bool {
	got := []byte(presented)
	var match int
	for _, want := range accepted {
		match |= subtle.ConstantTimeCompare(want, got)
	}
	return match == 1
}

// bearerCredentials sends a join token on every RPC of the config-sync stream.
//
// RequireTransportSecurity is false because a join token is precisely the
// mechanism for the case where TLS is absent. That combination is not silently
// allowed, though: an unencrypted, non-loopback channel is refused at startup
// unless the operator has configured a token (see GuardPlaintextDial), and it
// warns that the token itself crosses the wire in the clear and is replayable.
type bearerCredentials struct{ token string }

func (b bearerCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{authMetadataKey: bearerPrefix + b.token}, nil
}

func (b bearerCredentials) RequireTransportSecurity() bool { return false }
