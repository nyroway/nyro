package database

import (
	"context"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/infra/database/sqlite"
)

func TestParseDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dsn        string
		wantKind   Kind
		wantNative string
		wantErr    bool
		secret     string
	}{
		{name: "sqlite absolute path", dsn: "sqlite:///abs/x.db", wantKind: KindSQLite, wantNative: "/abs/x.db"},
		{name: "sqlite relative path", dsn: "sqlite://./x.db", wantKind: KindSQLite, wantNative: "./x.db"},
		{name: "sqlite memory", dsn: "sqlite://:memory:", wantKind: KindSQLite, wantNative: ":memory:"},
		{name: "postgres passthrough", dsn: "postgres://user:pass@host:5432/db?sslmode=disable", wantKind: KindPostgres, wantNative: "postgres://user:pass@host:5432/db?sslmode=disable"},
		{name: "postgresql alias rejected", dsn: "postgresql://user:pass@host:5432/db", wantErr: true, secret: "pass"},
		{name: "mysql rejected", dsn: "mysql://user:pass@host/db", wantErr: true, secret: "pass"},
		{name: "keyword DSN rejected without leaking", dsn: "host=localhost user=nyro secret=top-secret", wantErr: true, secret: "top-secret"},
		{name: "empty rejected", dsn: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kind, native, err := ParseDSN(tt.dsn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDSN(%q) error = nil", tt.dsn)
				}
				if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
					t.Fatalf("ParseDSN(%q) error leaks credentials: %v", tt.dsn, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDSN(%q) error = %v", tt.dsn, err)
			}
			if kind != tt.wantKind || native != tt.wantNative {
				t.Fatalf("ParseDSN(%q) = (%q, %q), want (%q, %q)", tt.dsn, kind, native, tt.wantKind, tt.wantNative)
			}
		})
	}
}

func TestOpenSQLiteAppliesDriverOptionsAndOwnsClose(t *testing.T) {
	conn, err := Open(context.Background(), "sqlite://:memory:", Options{
		SQLite: sqlite.Options{MaxOpenConns: 5, MaxIdleConns: 2},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if conn.Kind != KindSQLite {
		t.Fatalf("Kind = %q, want %q", conn.Kind, KindSQLite)
	}
	if got := conn.DB.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("MaxOpenConnections = %d, want 5", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := conn.DB.Ping(); err == nil {
		t.Fatal("Ping() after Close() error = nil")
	}
}

func TestOpenPostgresParseErrorDoesNotLeakPassword(t *testing.T) {
	const dsn = "postgres://user:top-secret@127.0.0.1:not-a-port/db"

	conn, err := Open(context.Background(), dsn, Options{})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("Open() error = nil")
	}
	if strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), dsn) {
		t.Fatalf("Open() error leaks credentials: %v", err)
	}
}
