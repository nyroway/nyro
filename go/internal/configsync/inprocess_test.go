package configsync

import (
	"context"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
)

// TestServeInProcess_SubscriberIsLabelledInProcess covers the embedded data
// plane's full path: a real ConfigClient dials a real ConfigServer over the
// in-memory pipe, receives a real snapshot, and shows up in the node list —
// labelled "inprocess" rather than "plaintext", since there is no socket and
// nothing external could reach the stream.
func TestServeInProcess_SubscriberIsLabelledInProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, _, _, _, _, _ := newPopulatedStorage(t)
	srv := NewConfigServer(st.Storage())
	dialOpts, shutdown := ServeInProcess(ctx, srv)
	defer shutdown()

	cache := &configsnapshot.Cache{}
	client := NewConfigClient(InProcessTarget, cacheSnapshotApplier{cache: cache}, "19530", nil)
	client.SetDialOptions(dialOpts...)
	go func() { _ = client.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for !cache.Ready() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the in-process client to apply a snapshot")
		case <-time.After(10 * time.Millisecond):
		}
	}

	nodes := srv.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("connected nodes = %d, want 1", len(nodes))
	}
	if nodes[0].ConnMode != ConnModeInProcess {
		t.Errorf("conn_mode = %q, want %q", nodes[0].ConnMode, ConnModeInProcess)
	}
	if nodes[0].ServicePort != "19530" {
		t.Errorf("service_port = %q, want 19530", nodes[0].ServicePort)
	}
}
