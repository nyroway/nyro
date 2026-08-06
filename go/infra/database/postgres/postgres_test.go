package postgres

import (
	"context"
	"strings"
	"testing"
)

func TestConnectionConfigNormalizesTimeZone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dsn      string
		wantZone string
	}{
		{name: "URL TimeZone", dsn: "postgres://user:pass@localhost/db?TimeZone=Asia%2FShanghai", wantZone: "Asia/Shanghai"},
		{name: "keyword time_zone", dsn: "host=localhost user=user password=pass dbname=db time_zone=Europe/London", wantZone: "Europe/London"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config, location, err := connectionConfig(tt.dsn)
			if err != nil {
				t.Fatalf("connectionConfig() error = %v", err)
			}
			if got := config.RuntimeParams["timezone"]; got != tt.wantZone {
				t.Fatalf("runtime timezone = %q, want %q", got, tt.wantZone)
			}
			if got := location.String(); got != tt.wantZone {
				t.Fatalf("scan location = %q, want %q", got, tt.wantZone)
			}
			if _, ok := config.RuntimeParams["TimeZone"]; ok {
				t.Fatal("mixed-case TimeZone runtime parameter was not normalized")
			}
			if _, ok := config.RuntimeParams["time_zone"]; ok {
				t.Fatal("time_zone runtime parameter was not normalized")
			}
		})
	}
}

func TestConnectionConfigWithoutTimeZoneUsesDefaultTimestampCodec(t *testing.T) {
	t.Parallel()

	_, location, err := connectionConfig("postgres://user:pass@localhost/db")
	if err != nil {
		t.Fatalf("connectionConfig() error = %v", err)
	}
	if location != nil {
		t.Fatalf("scan location = %v, want nil", location)
	}
}

func TestOpenHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := Open(ctx, Options{DSN: "postgres://user:pass@localhost/db"})
	if db != nil {
		_ = db.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Open() error = %v, want context cancellation", err)
	}
}

func TestConnectionConfigErrorDoesNotLeakPassword(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://user:top-secret@localhost:not-a-port/db"
	_, _, err := connectionConfig(dsn)
	if err == nil {
		t.Fatal("connectionConfig() error = nil")
	}
	if strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), dsn) {
		t.Fatalf("connectionConfig() error leaks credentials: %v", err)
	}
}
