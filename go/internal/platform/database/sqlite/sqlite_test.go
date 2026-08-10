package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsEmptyPath(t *testing.T) {
	t.Parallel()

	db, err := Open(context.Background(), Options{})
	if err == nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal("Open() error = nil, want an error for an empty path")
	}
}

func TestOpenConfiguresFileDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infra.db")
	db, err := Open(context.Background(), Options{
		Path:         path,
		BusyTimeout:  2 * time.Second,
		MaxOpenConns: 3,
		MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", got)
	}

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "2000",
		"synchronous":  "1",
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatalf("query PRAGMA %s: %v", name, err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
		}
	}
}

func TestOpenSupportsSharedMemoryDatabase(t *testing.T) {
	db, err := Open(context.Background(), Options{Path: "file:infra-test?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE sample (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestAnonymousMemoryDatabasesAreIsolated(t *testing.T) {
	first, err := Open(context.Background(), Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if _, err := first.Exec(`CREATE TABLE only_in_first (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create first table: %v", err)
	}

	second, err := Open(context.Background(), Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.QueryRow(`SELECT COUNT(*) FROM only_in_first`).Scan(new(int)); err == nil {
		t.Fatal("separate :memory: pools unexpectedly shared schema")
	}
}

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "canceled.db")})
	if err == nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal("Open() error = nil, want context cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Open() error = %q, want context cancellation", err)
	}
}
