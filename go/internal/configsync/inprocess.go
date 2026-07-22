package configsync

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
)

// InProcessTarget is the dial target used for the in-process config-sync
// channel. gRPC still requires a resolvable target string even when a custom
// dialer bypasses the network entirely, so this is a passthrough name that
// never reaches DNS.
const InProcessTarget = "passthrough:///nyro-inproc-configsync"

// inProcessBufSize is the bufconn pipe buffer. Snapshots are pushed as single
// messages and the client reads them promptly, so this only needs to absorb one
// in-flight snapshot rather than a backlog.
const inProcessBufSize = 1 << 20

// inProcessNetwork is the network name a bufconn listener reports for its
// address. StreamConfig uses it to label an embedded subscriber "inprocess"
// rather than "plaintext": both lack TLS state, but only one of them is a
// socket, and reporting an in-memory pipe as plaintext would push operators
// (and the WebUI's unauthenticated-node badge) to chase a nonexistent exposure.
const inProcessNetwork = "bufconn"

// ConnModeInProcess is the conn_mode reported for the embedded data plane's
// subscription. Distinct from "plaintext" (an unencrypted socket) and from
// "mtls"/"tls": there is no transport to secure and nothing external can reach
// this stream.
const ConnModeInProcess = "inprocess"

// ServeInProcess serves srv over an in-memory pipe instead of a network
// listener, and returns the dial options a ConfigClient needs to reach it.
//
// This is what lets `nyro server` run an embedded data plane over the SAME
// config-sync code path as a standalone `nyro proxy`: the embedded proxy builds
// a real ConfigClient, receives real ConfigSnapshot messages, and fills a real
// ConfigCache. Only the dial target differs. The alternative — having the
// embedded data plane read storage directly — would have created a second
// config path that could drift from the distributed one.
//
// Because the pipe never touches the network stack there is no listening
// socket, nothing external can dial it, and no bytes cross a host boundary.
// The config-sync transport security rules (TLS, join tokens, the non-loopback
// plaintext gate) therefore do not apply to this channel and must not be
// enforced against it — requiring a token here would break the single-binary
// zero-config path for no security gain.
//
// The returned shutdown function stops the server gracefully, with the same
// drain-then-stop behaviour as ServeGRPC.
func ServeInProcess(ctx context.Context, srv pb.ConfigServiceServer) (dialOpts []grpc.DialOption, shutdown func()) {
	lis := bufconn.Listen(inProcessBufSize)
	stop := serveOn(ctx, lis, srv)
	return []grpc.DialOption{
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			// The pipe has no transport to secure; insecure credentials here mean
			// "no TLS handshake over an in-memory pipe", not "plaintext on a wire".
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}, func() {
			stop()
			_ = lis.Close()
		}
}
