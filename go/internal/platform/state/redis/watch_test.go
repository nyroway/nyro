package redis

import (
	"fmt"
	"testing"
)

func TestWatchVersionsAreReleasedAfterHighCardinalityChurn(t *testing.T) {
	server := &Server{
		versions: make(map[string]uint64),
		watchers: make(map[string]uint64),
	}
	conn := &connectionState{}

	for i := range 10_000 {
		key := fmt.Sprintf("quota:requests:m:%d", i)
		server.trackWatchLocked(conn, key, false)
		server.bumpVersions([]string{key})
		server.clearWatchesLocked(conn)
	}

	if got := len(server.versions); got != 0 {
		t.Fatalf("tracked WATCH versions = %d, want 0 after churn", got)
	}
	if got := len(server.watchers); got != 0 {
		t.Fatalf("tracked WATCH refcounts = %d, want 0 after churn", got)
	}
}

func TestWatchVersionIsRetainedUntilLastWatcherClears(t *testing.T) {
	server := &Server{
		versions: make(map[string]uint64),
		watchers: make(map[string]uint64),
	}
	first := &connectionState{}
	second := &connectionState{}
	server.trackWatchLocked(first, "shared", true)
	server.trackWatchLocked(second, "shared", true)
	server.bumpVersions([]string{"shared"})

	server.clearWatchesLocked(first)
	if got := server.watchers["shared"]; got != 1 {
		t.Fatalf("watcher refcount = %d, want 1", got)
	}
	if !server.watchedChanged(second) {
		t.Fatal("remaining watcher did not observe the mutation")
	}

	server.clearWatchesLocked(second)
	if _, exists := server.versions["shared"]; exists {
		t.Fatal("WATCH version retained after last watcher cleared")
	}
}
