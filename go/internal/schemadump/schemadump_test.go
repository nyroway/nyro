package schemadump

import (
	"strings"
	"testing"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/storage/model"
)

func memDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// TestDumpRendersAllTablesWithoutExecuting is the critical check: DryRun +
// capture logger must emit every table's CREATE, and must NOT create anything.
func TestDumpRendersAllTablesWithoutExecuting(t *testing.T) {
	db := memDB(t)

	out, err := Dump(db)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	for _, table := range []string{"upstreams", "routes", "route_upstreams", "consumers", "consumer_keys", "consumer_routes", "consumer_quotas", "settings"} {
		if !strings.Contains(out, table) {
			t.Fatalf("dump missing table %q:\n%s", table, out)
		}
	}
	if !strings.Contains(strings.ToUpper(out), "CREATE TABLE") {
		t.Fatalf("dump has no CREATE TABLE:\n%s", out)
	}
	// DryRun must not have created anything.
	if db.Migrator().HasTable("upstreams") {
		t.Fatal("Dump executed DDL — DryRun should not touch the database")
	}
}

func TestDiffFromEmptyEmitsAllTables(t *testing.T) {
	shadow := memDB(t)
	out, err := Diff(shadow, "") // empty current schema → everything is new
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, table := range []string{"upstreams", "routes", "consumers", "settings"} {
		if !strings.Contains(out, table) {
			t.Fatalf("diff-from-empty missing %q:\n%s", table, out)
		}
	}
	if !strings.Contains(strings.ToUpper(out), "CREATE TABLE") {
		t.Fatalf("expected CREATE TABLE:\n%s", out)
	}
}

func TestDiffNoChangeEmitsNothing(t *testing.T) {
	shadow := memDB(t)
	// Current schema = the models' own full schema (via Dump). Diffing it
	// against the models should yield no new DDL.
	full, err := Dump(shadow)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	out, err := Diff(shadow, full)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(strings.ToUpper(out), "CREATE TABLE") {
		t.Fatalf("no-change diff should not create tables:\n%s", out)
	}
}

func TestDiffDetectsMissingTable(t *testing.T) {
	shadow := memDB(t)
	full, err := Dump(shadow)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	// Drop the "settings" CREATE from the current schema → diff should re-add it.
	var kept []string
	for _, stmt := range SplitStatements(full) {
		if !strings.Contains(stmt, "settings") {
			kept = append(kept, stmt)
		}
	}
	current := strings.Join(kept, ";\n") + ";"
	out, err := Diff(shadow, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "settings") {
		t.Fatalf("diff should re-create the missing settings table:\n%s", out)
	}
}

func TestSplitStatementsSkipsPsqlMetaCommands(t *testing.T) {
	// Real pg_dump (pg16) emits \restrict/\unrestrict meta-commands, SET lines,
	// and -- comments alongside the DDL.
	script := `--
-- PostgreSQL database dump
--
\restrict SomeOpaqueToken
SET statement_timeout = 0;
CREATE TABLE "upstreams" ("id" text, PRIMARY KEY ("id"));
\unrestrict SomeOpaqueToken
`
	stmts := SplitStatements(script)
	for _, s := range stmts {
		if strings.HasPrefix(s, `\`) || strings.HasPrefix(s, "--") {
			t.Fatalf("meta-command/comment leaked into statements: %q", s)
		}
	}
	var hasCreate bool
	for _, s := range stmts {
		if strings.Contains(s, "CREATE TABLE") {
			hasCreate = true
		}
	}
	if !hasCreate {
		t.Fatalf("CREATE TABLE dropped: %#v", stmts)
	}
}

func TestIntrospectSchemaRoundTrip(t *testing.T) {
	target := memDB(t)
	if err := target.AutoMigrate(model.All()...); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	// Introspect the target's current columns (incl. reserved names key/window).
	current, err := IntrospectSchema(target)
	if err != nil {
		t.Fatalf("IntrospectSchema: %v", err)
	}
	// Faithful capture: every table, a representative column set, the primary
	// key, and dialect-quoting of reserved names (key/window) are present.
	for _, want := range []string{
		"upstreams", "settings", "consumer_quotas",
		"`proxy_url`", "`credentials_json`", "`quota_limit`",
		"`key`", "`window`", // reserved words must be quoted
		"PRIMARY KEY (`id`)", "PRIMARY KEY (`key`)",
		"PRIMARY KEY (`consumer_id`, `route_id`)", // composite PK
		"DEFAULT", // column defaults reconstructed
		"CREATE UNIQUE INDEX `idx_upstreams_name`", // secondary indexes reconstructed
	} {
		if !strings.Contains(current, want) {
			t.Fatalf("introspection missing %q:\n%s", want, current)
		}
	}
	// The introspected script must be loadable (Diff execs it into a shadow).
	// On sqlite, type-affinity round-tripping (bool→numeric) can still trigger
	// a recreate, so we only assert it runs without error — precise diff
	// cleanliness against mysql/postgres is covered by the docker e2e.
	shadow := memDB(t)
	if _, err := Diff(shadow, current); err != nil {
		t.Fatalf("Diff against introspected schema errored: %v", err)
	}
}
