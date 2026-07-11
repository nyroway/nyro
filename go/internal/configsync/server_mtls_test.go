package configsync

import (
	"context"
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

// mtlsFixture is a self-signed CA plus an admin server leaf cert and a
// gateway client leaf cert (identity "cert-node"), all issued for these
// mTLS-focused tests.
type mtlsFixture struct {
	caPath                        string
	serverCertPath, serverKeyPath string
	clientCertPath, clientKeyPath string
}

func newMTLSFixture(t *testing.T) mtlsFixture {
	t.Helper()
	dir := t.TempDir()
	ca, err := pki.EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, serverKey, err := ca.SignServer(dir, "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, clientKey, err := ca.SignClient(dir, "gateway", "cert-node", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return mtlsFixture{
		caPath:         filepath.Join(dir, "ca.pem"),
		serverCertPath: serverCert,
		serverKeyPath:  serverKey,
		clientCertPath: clientCert,
		clientKeyPath:  clientKey,
	}
}

// bufconnMTLSEnv wires a ConfigServer behind an mTLS-secured bufconn
// listener (mirrors bufconnEnv in server_test.go, but with ServeGRPC's TLS
// server option instead of a bare grpc.NewServer()).
func bufconnMTLSEnv(t *testing.T, srv *ConfigServer, fx mtlsFixture) (dial func(t *testing.T, clientTLS *tls.Config) (pb.ConfigService_StreamConfigClient, func()), teardown func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	serverTLS, err := pki.LoadServerTLS(fx.caPath, fx.serverCertPath, fx.serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	pb.RegisterConfigServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()

	dial = func(t *testing.T, clientTLS *tls.Config) (pb.ConfigService_StreamConfigClient, func()) {
		t.Helper()
		dialOpt := grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})
		conn, err := grpc.NewClient("passthrough:///bufnet", dialOpt, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		stream, err := pb.NewConfigServiceClient(conn).StreamConfig(context.Background(), &pb.Subscribe{
			Version: 0,
			NodeId:  "self-reported-should-be-overridden",
		})
		if err != nil {
			_ = conn.Close()
			return nil, func() {}
		}
		return stream, func() { _ = conn.Close() }
	}
	return dial, gs.GracefulStop
}

func TestStreamConfig_MTLS_IdentityFromCertOverridesSelfReported(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	fx := newMTLSFixture(t)
	srv := NewConfigServer(st.Storage())
	dial, stop := bufconnMTLSEnv(t, srv, fx)
	defer stop()

	clientTLS, err := pki.LoadClientTLS(fx.caPath, fx.clientCertPath, fx.clientKeyPath)
	if err != nil {
		t.Fatal(err)
	}

	stream, closeFn := dial(t, clientTLS)
	if stream == nil {
		t.Fatal("expected successful mTLS StreamConfig")
	}
	defer closeFn()

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	waitFor(t, time.Second, func() bool { return len(srv.Nodes()) == 1 })

	nodes := srv.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("Nodes() len = %d; want 1", len(nodes))
	}
	if nodes[0].NodeID != "cert-node" {
		t.Errorf("NodeID = %q; want %q (identity must come from the verified client cert, not Subscribe.node_id)", nodes[0].NodeID, "cert-node")
	}
}

func TestStreamConfig_MTLS_RejectsWrongCACert(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	fx := newMTLSFixture(t)
	srv := NewConfigServer(st.Storage())
	dial, stop := bufconnMTLSEnv(t, srv, fx)
	defer stop()

	otherDir := t.TempDir()
	otherCA, err := pki.EnsureCA(otherDir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	otherCertPath, otherKeyPath, err := otherCA.SignClient(otherDir, "gateway", "cert-node", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	clientTLS, err := pki.LoadClientTLS(fx.caPath, otherCertPath, otherKeyPath)
	if err != nil {
		t.Fatal(err)
	}

	stream, closeFn := dial(t, clientTLS)
	defer closeFn()
	if stream != nil {
		if _, err := stream.Recv(); err == nil {
			t.Fatal("expected mTLS handshake/stream to fail for a client cert signed by a different CA")
		}
	}
}

func TestStreamConfig_MTLS_RejectsNoClientCert(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	fx := newMTLSFixture(t)
	srv := NewConfigServer(st.Storage())
	dial, stop := bufconnMTLSEnv(t, srv, fx)
	defer stop()

	// Bypass identity verification here (InsecureSkipVerify) so the
	// handshake actually reaches the point where the server enforces
	// RequireAndVerifyClientCert — this test is specifically about the
	// server rejecting a missing client cert.
	noCertTLS := &tls.Config{InsecureSkipVerify: true}

	stream, closeFn := dial(t, noCertTLS)
	defer closeFn()
	if stream != nil {
		if _, err := stream.Recv(); err == nil {
			t.Fatal("expected mTLS handshake/stream to fail with no client certificate presented")
		}
	}
}

func TestStreamConfig_Tier0_PlaintextStillUsesSelfReportedNodeID(t *testing.T) {
	// Regression: with tlsConfig=nil (Tier 0), peer.AuthInfo carries no TLS
	// state, so identity derivation must be a no-op and self-reported
	// node_id must pass through exactly as before this change.
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	stream, closeFn := dialClient(t, dialOpt)
	defer closeFn()

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	waitFor(t, time.Second, func() bool { return len(srv.Nodes()) == 1 })

	nodes := srv.Nodes()
	if len(nodes) != 1 || nodes[0].NodeID != "test-node-1" {
		t.Fatalf("Nodes() = %+v; want self-reported node_id %q preserved under Tier 0", nodes, "test-node-1")
	}
}
