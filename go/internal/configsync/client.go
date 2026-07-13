package configsync

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/version"
)

// ConfigClient connects to the admin's gRPC ConfigService, subscribes to the
// config stream, and swaps each received snapshot into the cache. It reconnects
// with exponential backoff on drop, always re-subscribing at version=0 (full
// resync) — this is the simplest correct option and matches the full-snapshot
// push model.
type ConfigClient struct {
	target string
	cache  *ConfigCache

	// Node identity, generated once at construction and reused across
	// reconnects so the admin's node registry sees a stable identity rather
	// than a new "node" per reconnect. Purely for operational visibility
	// (/api/v1/nodes); never influences what config is served.
	nodeID      string
	appVersion  string
	hostname    string
	servicePort string

	// dialOpts allow tests to inject a bufconn dialer.
	dialOpts []grpc.DialOption

	// Backoff config. Overridable for tests.
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// NewConfigClient builds a client that connects to target (host:port) and
// publishes received snapshots to cache. servicePort is this gateway's own
// data-plane listen port (for node-visibility reporting only — pass "" if
// unknown/not applicable, e.g. in tests).
//
// A nil tlsConfig selects plaintext. The gateway CLI reaches that mode only
// when all three --config-tls-* paths are empty and logs a security warning
// before constructing the client. When set (see pki.LoadClientTLS), the client
// authenticates itself to admin with a client certificate and verifies admin's
// server certificate against the configured CA.
func NewConfigClient(target string, cache *ConfigCache, servicePort string, tlsConfig *tls.Config) *ConfigClient {
	creds := insecure.NewCredentials()
	if tlsConfig != nil {
		creds = credentials.NewTLS(tlsConfig)
	}
	return &ConfigClient{
		target:         target,
		cache:          cache,
		nodeID:         newNodeID(),
		appVersion:     buildVersion(),
		hostname:       hostnameOrUnknown(),
		servicePort:    servicePort,
		initialBackoff: 500 * time.Millisecond,
		maxBackoff:     30 * time.Second,
		dialOpts: []grpc.DialOption{
			grpc.WithTransportCredentials(creds),
		},
	}
}

// newNodeID generates a per-process random identifier (survives reconnects,
// disambiguates multiple gateways on the same host/container image). It is
// deliberately host-agnostic: hostname is reported separately (Subscribe.
// hostname / NodeInfo.Hostname), so baking it into node_id too would just
// duplicate that column in the admin's node list.
func newNodeID() string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand failure is effectively unheard of; fall back to a
		// constant suffix rather than failing gateway startup over it.
		return "0000"
	}
	return hex.EncodeToString(suffix)
}

func hostnameOrUnknown() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	return h
}

// buildVersion returns the gateway's version for node-visibility purposes.
// version.Version is the project's authoritative version (see that package's
// doc comment for why this beats debug.ReadBuildInfo — the latter only
// carries a real semver for `go install pkg@version` builds).
func buildVersion() string {
	return version.Version
}

// Run blocks until ctx is cancelled, maintaining the stream and republishing
// snapshots. It logs disconnects and reconnects. Returns nil on context
// cancellation; any fatal dial errors are logged and the loop backs off.
func (c *ConfigClient) Run(ctx context.Context) error {
	var attempt int
	for {
		if ctx.Err() != nil {
			return nil
		}
		connected, err := c.runOnce(ctx)
		if connected {
			attempt = 0 // reset backoff after a successful connect+receive cycle
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("configsync client: stream ended: %v", err)
		} else {
			log.Printf("configsync client: stream ended")
		}
		attempt++
		backoff := c.backoff(attempt)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
	}
}

// runOnce dials, opens the stream, and blocks receiving snapshots until the
// stream breaks or ctx is cancelled. connected is true if the stream opened and
// received at least one snapshot (i.e. we genuinely connected).
func (c *ConfigClient) runOnce(ctx context.Context) (connected bool, err error) {
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := grpc.NewClient(c.target, c.dialOpts...)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewConfigServiceClient(conn)
	stream, err := client.StreamConfig(dialCtx, &pb.Subscribe{
		Version:     0,
		NodeId:      c.nodeID,
		AppVersion:  c.appVersion,
		Hostname:    c.hostname,
		ServicePort: c.servicePort,
	})
	if err != nil {
		return false, err
	}

	for {
		snap, err := stream.Recv()
		if err != nil {
			return connected, err
		}
		connected = true
		internal := SnapshotFromProto(snap)
		c.cache.Swap(internal)
		log.Printf("configsync client: applied snapshot v%d (upstreams=%d routes=%d consumers=%d)",
			snap.GetVersion(), len(snap.GetUpstreams()), len(snap.GetRoutes()), len(snap.GetConsumers()))
	}
}

// backoff returns a growing delay for attempt (1-based), capped at maxBackoff.
func (c *ConfigClient) backoff(attempt int) time.Duration {
	if attempt <= 1 {
		return c.initialBackoff
	}
	// exponential: base * 2^(attempt-1), capped.
	mult := math.Pow(2, float64(attempt-1))
	d := time.Duration(float64(c.initialBackoff) * mult)
	if d > c.maxBackoff {
		d = c.maxBackoff
	}
	return d
}

// ServeGRPC starts a gRPC server listening on addr serving srv. It blocks until
// the listener returns (always in a goroutine for ctx-driven shutdown). The
// returned shutdown function stops the server gracefully.
//
// A nil tlsConfig selects plaintext. The admin CLI reaches that mode only when
// all three --config-tls-* paths are empty and logs a security warning before
// starting the server. When set, tlsConfig must require and verify client
// certificates (see pki.LoadServerTLS) — the gateway's node identity is then
// derived from the verified client certificate's SPIFFE SAN rather than
// trusted from Subscribe.node_id (see StreamConfig).
func ServeGRPC(ctx context.Context, addr string, srv pb.ConfigServiceServer, tlsConfig *tls.Config) (shutdown func(), err error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		// If the address looks like it lacks a port, surface a clearer error.
		if strings.Contains(err.Error(), "address") {
			return nil, errors.New("grpc listen " + addr + ": " + err.Error())
		}
		return nil, err
	}
	var opts []grpc.ServerOption
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	gs := grpc.NewServer(opts...)
	pb.RegisterConfigServiceServer(gs, srv)
	go func() {
		<-ctx.Done()
		stopped := make(chan struct{})
		go func() { gs.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			gs.Stop()
		}
	}()
	go func() {
		if err := gs.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Printf("configsync grpc server: %v", err)
		}
	}()
	return gs.GracefulStop, nil
}
