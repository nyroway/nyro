package configsync

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// bufconnMTLSDialer starts an mTLS-secured ConfigServer behind bufconn and
// returns a plain (non-credentialed) DialOption for reaching it — the caller
// still has to supply transport credentials via ConfigClient.dialOpts,
// exactly like a real gateway would via NewConfigClient's tlsConfig param.
func bufconnMTLSDialer(t *testing.T, store *memory.Backend, fx mtlsFixture) (dialOpt grpc.DialOption, teardown func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	serverTLS, err := pki.LoadServerTLS(fx.caPath, fx.serverCertPath, fx.serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewConfigServer(store.Storage())
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	pb.RegisterConfigServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dialOpt = grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
	return dialOpt, gs.GracefulStop
}

func TestConfigClient_MTLS_ReceivesOverSecureChannel(t *testing.T) {
	st, _, rOpen, _, _, _ := newPopulatedStorage(t)
	fx := newMTLSFixture(t)
	dialOpt, stop := bufconnMTLSDialer(t, st, fx)
	defer stop()

	clientTLS, err := pki.LoadClientTLS(fx.caPath, fx.clientCertPath, fx.clientKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	cache := &ConfigCache{}
	c := NewConfigClient("passthrough:///bufnet", cache, "19530", clientTLS)
	c.initialBackoff = 20 * time.Millisecond
	c.maxBackoff = 100 * time.Millisecond
	c.dialOpts = []grpc.DialOption{dialOpt, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS))}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	waitFor(t, 2*time.Second, func() bool {
		return cache.Ready() && cache.Load().RouteByModel(rOpen.Model) != nil
	})
}

func TestConfigClient_MTLS_WrongCARejected(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	fx := newMTLSFixture(t)
	dialOpt, stop := bufconnMTLSDialer(t, st, fx)
	defer stop()

	otherDir := t.TempDir()
	// A second, unrelated CA that did NOT sign admin's server cert. The
	// client below trusts only this CA, so it must reject admin's server
	// certificate during the TLS handshake.
	if _, err := pki.EnsureCA(otherDir, time.Hour); err != nil {
		t.Fatal(err)
	}
	wrongCAPath := filepath.Join(otherDir, "ca.pem")

	clientTLS, err := pki.LoadClientTLS(wrongCAPath, fx.clientCertPath, fx.clientKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	cache := &ConfigCache{}
	c := NewConfigClient("passthrough:///bufnet", cache, "19530", clientTLS)
	c.initialBackoff = 20 * time.Millisecond
	c.maxBackoff = 50 * time.Millisecond
	c.dialOpts = []grpc.DialOption{dialOpt, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS))}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)

	if cache.Ready() {
		t.Fatal("expected cache to remain unfilled when the server cert isn't trusted")
	}
}
