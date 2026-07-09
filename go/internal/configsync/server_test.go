package configsync

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// bufconnEnv wires a ConfigServer behind a bufconn listener and returns a
// client dial option + a teardown.
func bufconnEnv(t *testing.T, store *memory.Backend) (srv *ConfigServer, dialOpt grpc.DialOption, teardown func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv = NewConfigServer(store.Storage())
	gs := grpc.NewServer()
	pb.RegisterConfigServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dialOpt = grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
	teardown = gs.GracefulStop
	return srv, dialOpt, teardown
}

// dialClient opens a StreamConfig(version=0) against the bufconn env.
func dialClient(t *testing.T, dialOpt grpc.DialOption) (pb.ConfigService_StreamConfigClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream, err := pb.NewConfigServiceClient(conn).StreamConfig(context.Background(), &pb.Subscribe{
		Version:    0,
		NodeId:     "test-node-1",
		AppVersion: "test-version",
		Hostname:   "test-host",
	})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("StreamConfig: %v", err)
	}
	return stream, func() { _ = conn.Close() }
}

func TestStreamConfig_SendsInitialSnapshot(t *testing.T) {
	st, u, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	stream, closeFn := dialClient(t, dialOpt)
	defer closeFn()

	snap, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if snap.GetVersion() != 0 { // no bumpEpoch yet
		t.Errorf("initial version = %d; want 0", snap.GetVersion())
	}
	found := false
	for _, up := range snap.GetUpstreams() {
		if up.GetId() == u.ID {
			found = true
		}
	}
	if !found {
		t.Error("initial snapshot missing upstream")
	}
	if srv.ClientCount() != 1 {
		t.Errorf("ClientCount = %d; want 1", srv.ClientCount())
	}

	nodes := srv.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("Nodes() len = %d; want 1", len(nodes))
	}
	if nodes[0].NodeID != "test-node-1" || nodes[0].AppVersion != "test-version" || nodes[0].Hostname != "test-host" {
		t.Errorf("Nodes()[0] = %+v; want node_id/app_version/hostname from Subscribe", nodes[0])
	}
	if nodes[0].RemoteAddr == "" {
		t.Error("Nodes()[0].RemoteAddr is empty; want peer address")
	}
	if nodes[0].AppliedVersion != 0 {
		t.Errorf("Nodes()[0].AppliedVersion = %d; want 0 (initial snapshot)", nodes[0].AppliedVersion)
	}
}

// TestNodes_EmptyWhenNoClients proves the registry reflects only live streams.
func TestNodes_EmptyWhenNoClients(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv, _, stop := bufconnEnv(t, st)
	defer stop()

	if nodes := srv.Nodes(); len(nodes) != 0 {
		t.Errorf("Nodes() = %v; want empty before any client connects", nodes)
	}
}

// TestNodes_RemovedOnDisconnect proves a disconnected gateway disappears from
// the registry (no separate offline bookkeeping needed).
func TestNodes_RemovedOnDisconnect(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	stream, err := pb.NewConfigServiceClient(conn).StreamConfig(ctx, &pb.Subscribe{NodeId: "test-node-2"})
	if err != nil {
		t.Fatalf("StreamConfig: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("initial Recv: %v", err)
	}
	waitFor(t, time.Second, func() bool { return len(srv.Nodes()) == 1 })

	cancel()
	waitFor(t, time.Second, func() bool { return len(srv.Nodes()) == 0 })
}

func TestNotify_PushesToConnectedGateway(t *testing.T) {
	st, u, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	stream, closeFn := dialClient(t, dialOpt)
	defer closeFn()

	// initial snapshot (version 0)
	snap0, err := stream.Recv()
	if err != nil {
		t.Fatalf("initial Recv: %v", err)
	}
	if snap0.GetVersion() != 0 {
		t.Fatalf("initial version = %d; want 0", snap0.GetVersion())
	}

	// Add a route + bump epoch, then Notify (mirrors admin write path).
	if _, err := st.Storage().Routes().Create(storage.CreateRoute{Model: "new-model", Upstreams: []storage.CreateRouteUpstream{
		{UpstreamID: u.ID, Model: "nm"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Storage().Settings().Set("config_epoch", "3"); err != nil {
		t.Fatal(err)
	}
	srv.Notify()

	snap1, err := stream.Recv()
	if err != nil {
		t.Fatalf("post-Notify Recv: %v", err)
	}
	if snap1.GetVersion() != 3 {
		t.Errorf("pushed version = %d; want 3", snap1.GetVersion())
	}
	hasNew := false
	for _, r := range snap1.GetRoutes() {
		if r.GetModel() == "new-model" {
			hasNew = true
		}
	}
	if !hasNew {
		t.Error("pushed snapshot missing the newly-added route")
	}
}

// TestStreamConfig_DisconnectOnClientCancel verifies the server cleans up the
// client when the gateway cancels (reconnect scenario on the client side).
func TestStreamConfig_DisconnectOnClientCancel(t *testing.T) {
	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv, dialOpt, stop := bufconnEnv(t, st)
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	stream, err := pb.NewConfigServiceClient(conn).StreamConfig(ctx, &pb.Subscribe{Version: 0})
	if err != nil {
		t.Fatalf("StreamConfig: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("initial Recv: %v", err)
	}
	waitFor(t, time.Second, func() bool { return srv.ClientCount() == 1 })

	cancel()
	waitFor(t, time.Second, func() bool { return srv.ClientCount() == 0 })
}
