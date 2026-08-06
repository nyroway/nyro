package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	infradatabase "github.com/nyroway/nyro/go/infra/database"
	dbpostgres "github.com/nyroway/nyro/go/infra/database/postgres"
	"github.com/nyroway/nyro/go/internal/storage"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("NYRO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NYRO_TEST_POSTGRES_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConnection, err := infradatabase.Open(ctx, dsn, infradatabase.Options{})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	t.Cleanup(func() { _ = adminConnection.Close() })

	schema := fmt.Sprintf("nyro_go_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := adminConnection.DB.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create temporary schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminConnection.DB.ExecContext(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop temporary schema: %v", err)
		}
	})

	schemaDSN := postgresDSNWithSearchPath(t, dsn, schema)
	connection, err := infradatabase.Open(ctx, schemaDSN, infradatabase.Options{
		Postgres: dbpostgres.Options{MaxOpenConns: 7, MaxIdleConns: 3},
	})
	if err != nil {
		t.Fatalf("open schema-scoped PostgreSQL connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if got := connection.DB.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", got)
	}

	b, err := New(connection.Kind, connection.DB)
	if err != nil {
		t.Fatalf("create PostgreSQL backend: %v", err)
	}
	if err := b.Migrate(); err != nil {
		t.Fatalf("migrate PostgreSQL backend: %v", err)
	}
	created, err := b.Upstreams().Create(storage.CreateUpstream{
		Name:     "postgres-integration",
		Protocol: "openai-chatcompletions",
		BaseURL:  "https://api.example.com/v1",
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	got, err := b.Upstreams().Get(created.ID)
	if err != nil {
		t.Fatalf("get upstream: %v", err)
	}
	if got == nil || got.Name != "postgres-integration" {
		t.Fatalf("upstream = %+v", got)
	}
	health, err := b.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.Backend != "postgres" || !health.CanConnect || !health.SchemaCompatible || !health.Writable {
		t.Fatalf("health = %+v", health)
	}
}

func postgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse NYRO_TEST_POSTGRES_DSN: %v", err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}
