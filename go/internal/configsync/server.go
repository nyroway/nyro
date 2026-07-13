package configsync

import (
	"log"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/nyroway/nyro/go/internal/configsync/pb/configsync/v1"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/storage"
)

// ConfigServer implements pb.ConfigServiceServer. It serves the current
// snapshot to every connected gateway stream and pushes a fresh full snapshot
// whenever Notify is called after a config write.
//
// The "current snapshot" is rebuilt from storage on demand so the server never
// holds a stale copy: a gateway connecting between Notify calls still gets the
// freshest DB state. The epoch (config_version) is read from storage at build
// time so it matches what the admin REST handlers stamped via bumpEpoch.
type ConfigServer struct {
	pb.UnimplementedConfigServiceServer

	store storage.Storage

	mu       sync.Mutex
	current  *pb.ConfigSnapshot // last snapshot handed to Notify (nil until first push)
	clients  map[*streamClient]struct{}
	buildCnt int // diagnostics: number of snapshot rebuilds
}

type streamClient struct {
	// ch receives snapshots to push. Buffered (1) so the broadcaster never
	// blocks on a slow client; a full channel means the client is lagging and
	// gets dropped on the next push.
	ch chan *pb.ConfigSnapshot

	// Node identity/metadata, self-reported by the gateway in Subscribe and
	// captured at connect time. Used only for operational visibility (Nodes);
	// never persisted.
	nodeID      string
	appVersion  string
	hostname    string
	servicePort string
	remoteAddr  string
	connectedAt time.Time

	mu          sync.Mutex
	lastVersion int64 // version of the last snapshot successfully queued to ch
}

// NodeInfo is the admin-facing (read-only) view of one connected gateway
// stream, exposed via ConfigServer.Nodes for the /api/v1/nodes admin endpoint.
type NodeInfo struct {
	NodeID         string    `json:"node_id"`
	Hostname       string    `json:"hostname"`
	AppVersion     string    `json:"app_version"`
	ServicePort    string    `json:"service_port"`
	RemoteAddr     string    `json:"remote_addr"`
	ConnectedAt    time.Time `json:"connected_at"`
	AppliedVersion int64     `json:"applied_version"`
}

// NewConfigServer creates a ConfigServiceServer backed by store.
func NewConfigServer(store storage.Storage) *ConfigServer {
	return &ConfigServer{
		store:   store,
		clients: map[*streamClient]struct{}{},
	}
}

// StreamConfig implements the long-lived gateway subscription. The gateway
// sends a Subscribe{version, node_id, app_version, hostname}; the server
// immediately pushes the current full snapshot, then pushes a fresh snapshot
// on every Notify. The stream stays open until the gateway disconnects or the
// context is cancelled. Node identity from req is captured for operational
// visibility only — it never gates or shapes what gets pushed.
func (s *ConfigServer) StreamConfig(req *pb.Subscribe, stream grpc.ServerStreamingServer[pb.ConfigSnapshot]) error {
	ctx := stream.Context()
	remoteAddr := ""
	nodeID := req.GetNodeId()
	if p, ok := peer.FromContext(ctx); ok {
		if p.Addr != nil {
			remoteAddr = p.Addr.String()
		}
		// Under mTLS, the peer's verified client certificate is the
		// authoritative source of node identity — it overrides whatever the
		// gateway self-reported in Subscribe.node_id, which is otherwise
		// trivially spoofable. In plaintext mode, p.AuthInfo carries no TLS
		// state, so this is a no-op and the self-reported node_id passes through
		// unchanged.
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
			leaf := tlsInfo.State.VerifiedChains[0][0]
			if id, err := pki.GatewayNodeIDFromCert(leaf); err == nil {
				nodeID = id
			} else {
				log.Printf("configsync: client cert has no usable identity, keeping self-reported node_id: %v", err)
			}
		}
	}

	// Register BEFORE the initial push. This way, any Notify that arrives
	// during the initial build is queued on the client's buffered channel and
	// delivered right after the initial snapshot — no missed pushes, no double
	// initial push. The gateway dedups by version, so a follow-up is harmless.
	c := &streamClient{
		ch:          make(chan *pb.ConfigSnapshot, 1),
		nodeID:      nodeID,
		appVersion:  req.GetAppVersion(),
		hostname:    req.GetHostname(),
		servicePort: req.GetServicePort(),
		remoteAddr:  remoteAddr,
		connectedAt: time.Now(),
	}
	s.register(c)
	defer s.unregister(c)

	// Build + send the initial full snapshot.
	snap, err := s.build()
	if err != nil {
		return status.Errorf(codes.Unavailable, "build snapshot: %v", err)
	}
	if err := stream.Send(snap); err != nil {
		return err
	}
	c.setLastVersion(snap.GetVersion())

	for {
		select {
		case snap := <-c.ch:
			if err := stream.Send(snap); err != nil {
				return err
			}
			c.setLastVersion(snap.GetVersion())
		case <-ctx.Done():
			return nil
		}
	}
}

func (c *streamClient) setLastVersion(v int64) {
	c.mu.Lock()
	c.lastVersion = v
	c.mu.Unlock()
}

func (c *streamClient) getLastVersion() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastVersion
}

// build reads storage + epoch and returns a fresh wire snapshot. Also caches it
// as the last-pushed snapshot so Notify broadcasts a consistent object.
func (s *ConfigServer) build() (*pb.ConfigSnapshot, error) {
	s.mu.Lock()
	s.buildCnt++
	bc := s.buildCnt
	s.mu.Unlock()

	epoch := EpochFromStorage(s.store)
	snap, err := SnapshotFromStorage(s.store, epoch)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.current = snap
	s.mu.Unlock()
	log.Printf("configsync: built snapshot v%d (build #%d)", epoch, bc)
	return snap, nil
}

// Notify pushes a fresh snapshot to every connected gateway. Called by the
// admin after every config write (the same places that call bumpEpoch). It is
// safe to call concurrently; a slow client whose channel is full is dropped.
func (s *ConfigServer) Notify() {
	snap, err := s.build()
	if err != nil {
		log.Printf("configsync: Notify build failed: %v", err)
		return
	}
	s.mu.Lock()
	clients := make([]*streamClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		select {
		case c.ch <- snap:
		default:
			// Client is lagging: drop it so the stream returns and the gateway
			// reconnects with a full resync. Non-blocking so Notify stays fast.
			log.Printf("configsync: dropping lagging client (full push channel)")
			s.unregister(c)
		}
	}
}

func (s *ConfigServer) register(c *streamClient) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *ConfigServer) unregister(c *streamClient) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		// Best-effort close so a blocked sender notices.
		select {
		case <-c.ch:
		default:
		}
	}
	s.mu.Unlock()
}

// ClientCount returns the number of connected gateway streams (for tests/diagnostics).
func (s *ConfigServer) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// Nodes returns a snapshot of every currently connected gateway stream, for
// operational visibility (the admin's /api/v1/nodes endpoint). This is an
// in-memory, best-effort view: it reflects only streams alive right now and
// is never persisted — a gateway disappears from it the moment its stream
// drops, with no separate offline/heartbeat bookkeeping.
func (s *ConfigServer) Nodes() []NodeInfo {
	s.mu.Lock()
	clients := make([]*streamClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	out := make([]NodeInfo, 0, len(clients))
	for _, c := range clients {
		out = append(out, NodeInfo{
			NodeID:         c.nodeID,
			Hostname:       c.hostname,
			AppVersion:     c.appVersion,
			ServicePort:    c.servicePort,
			RemoteAddr:     c.remoteAddr,
			ConnectedAt:    c.connectedAt,
			AppliedVersion: c.getLastVersion(),
		})
	}
	return out
}

// CurrentVersion returns the version of the last-built snapshot (0 if none).
func (s *ConfigServer) CurrentVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return 0
	}
	return s.current.GetVersion()
}
