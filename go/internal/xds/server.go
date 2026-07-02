// Package xds is nyro's config-distribution plane: a custom gRPC
// ConfigService that pushes full config snapshots from the admin (control
// plane) to gateways (data plane). The name borrows Envoy's push model as a
// concept only — this is NOT the Envoy xDS protocol (no ADS, no
// delta/SotW variants, no ACK/NACK).
//
// This file also holds the gRPC control-plane server (ConfigService) that
// pushes configuration snapshots to connected gateways. The admin process runs
// one of these alongside its REST API; each gateway opens a long-lived
// StreamConfig stream and receives a full snapshot on connect and on every
// config change.
package xds

import (
	"log"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nyroway/nyro/go/internal/storage"
	pb "github.com/nyroway/nyro/go/internal/xds/pb/xds/v1"
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
}

// NewConfigServer creates a ConfigServiceServer backed by store.
func NewConfigServer(store storage.Storage) *ConfigServer {
	return &ConfigServer{
		store:   store,
		clients: map[*streamClient]struct{}{},
	}
}

// StreamConfig implements the long-lived gateway subscription. The gateway
// sends a Subscribe{version}; the server immediately pushes the current full
// snapshot, then pushes a fresh snapshot on every Notify. The stream stays open
// until the gateway disconnects or the context is cancelled.
func (s *ConfigServer) StreamConfig(req *pb.Subscribe, stream grpc.ServerStreamingServer[pb.ConfigSnapshot]) error {
	// Register BEFORE the initial push. This way, any Notify that arrives
	// during the initial build is queued on the client's buffered channel and
	// delivered right after the initial snapshot — no missed pushes, no double
	// initial push. The gateway dedups by version, so a follow-up is harmless.
	c := &streamClient{ch: make(chan *pb.ConfigSnapshot, 1)}
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

	ctx := stream.Context()
	for {
		select {
		case snap := <-c.ch:
			if err := stream.Send(snap); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
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
	log.Printf("xds: built snapshot v%d (build #%d)", epoch, bc)
	return snap, nil
}

// Notify pushes a fresh snapshot to every connected gateway. Called by the
// admin after every config write (the same places that call bumpEpoch). It is
// safe to call concurrently; a slow client whose channel is full is dropped.
func (s *ConfigServer) Notify() {
	snap, err := s.build()
	if err != nil {
		log.Printf("xds: Notify build failed: %v", err)
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
			log.Printf("xds: dropping lagging client (full push channel)")
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

// CurrentVersion returns the version of the last-built snapshot (0 if none).
func (s *ConfigServer) CurrentVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return 0
	}
	return s.current.GetVersion()
}
