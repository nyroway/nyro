package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	"github.com/nyroway/nyro/go/infra/state"
	statesqlite "github.com/nyroway/nyro/go/infra/state/sqlite"
)

func TestJanitorPhysicallyDeletesExpiredRowsInChunks(t *testing.T) {
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	store, err := statesqlite.New(ctx, db, statesqlite.Options{
		Now:              clock.Now,
		CleanupInterval:  5 * time.Millisecond,
		CleanupBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	for _, key := range []string{"a", "b"} {
		if _, err := store.Set(ctx, []byte(key), []byte("value"), state.SetOptions{Expiration: time.Second}); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	clock.Advance(2 * time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if physicalRowCount(t, db) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("janitor did not physically remove expired rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func physicalRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_kv`).Scan(&count); err != nil {
		t.Fatalf("count state rows: %v", err)
	}
	return count
}
