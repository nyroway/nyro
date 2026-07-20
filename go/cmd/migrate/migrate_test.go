package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run executes `nyro migrate <args...>` against an isolated command tree and
// returns combined stdout and any error.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestDumpDefaultSQLite(t *testing.T) {
	out, err := run(t, "dump") // no --dsn → sqlite in-memory
	if err != nil {
		t.Fatalf("dump: %v\n%s", err, out)
	}
	for _, table := range []string{"upstreams", "settings", "consumer_keys"} {
		if !strings.Contains(out, table) {
			t.Fatalf("dump missing %q:\n%s", table, out)
		}
	}
	if !strings.Contains(strings.ToUpper(out), "CREATE TABLE") {
		t.Fatalf("dump has no CREATE TABLE:\n%s", out)
	}
}

func TestDumpToOutputFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "schema.sql")
	if _, err := run(t, "dump", "--output", f); err != nil {
		t.Fatalf("dump --output: %v", err)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(b), "CREATE TABLE") {
		t.Fatalf("output file has no DDL:\n%s", b)
	}
}

func TestDiffFromEmptyFile(t *testing.T) {
	// Empty target-file = fresh database → diff should create everything.
	f := filepath.Join(t.TempDir(), "empty.sql")
	if err := os.WriteFile(f, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "diff", "--target-file", f) // shadow defaults to sqlite mem
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if !strings.Contains(strings.ToUpper(out), "CREATE TABLE") {
		t.Fatalf("diff-from-empty should create tables:\n%s", out)
	}
}

func TestDiffRequiresExactlyOneSource(t *testing.T) {
	// Neither source.
	if _, err := run(t, "diff"); err == nil {
		t.Fatal("diff with no source: want error")
	}
	// Both sources.
	f := filepath.Join(t.TempDir(), "s.sql")
	_ = os.WriteFile(f, []byte(""), 0o644)
	if _, err := run(t, "diff", "--target-file", f, "--target-dsn", "sqlite://:memory:"); err == nil {
		t.Fatal("diff with both sources: want error")
	}
}
