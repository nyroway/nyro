// Package schemadump renders and diffs the canonical GORM schema
// (internal/storage/model) into plain SQL, using only GORM — no Atlas, no
// external framework.
//
//   - Dump renders the full CREATE DDL for model.All() in a connection's
//     dialect, via a DryRun session so nothing is executed (the connection may
//     be read-only). Used by `nyro migrate dump`.
//   - Diff replays a "current" schema onto a writable shadow database, runs
//     AutoMigrate(model.All()) for real, and captures the incremental DDL it
//     issues. Used by `nyro migrate diff`.
//
// Both work by capturing the SQL GORM's migrator builds via a logger, then
// keeping only the DDL statements (CREATE/ALTER/DROP).
package schemadump

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/nyroway/nyro/go/internal/storage/model"
)

// captureLogger records every SQL statement GORM traces. GORM calls Trace for
// each built statement even under DryRun, so this captures the migrator's DDL
// without the statements being executed.
type captureLogger struct {
	stmts *[]string
}

func (c captureLogger) LogMode(logger.LogLevel) logger.Interface { return c }
func (captureLogger) Info(context.Context, string, ...any)       {}
func (captureLogger) Warn(context.Context, string, ...any)       {}
func (captureLogger) Error(context.Context, string, ...any)      {}
func (c captureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	if sql != "" {
		*c.stmts = append(*c.stmts, sql)
	}
}

// Dump returns the full CREATE DDL for model.All() rendered in db's dialect.
// It runs in a DryRun session, so db is never written to (a read-only
// connection is fine); db only supplies the dialect.
func Dump(db *gorm.DB) (string, error) {
	var stmts []string
	session := db.Session(&gorm.Session{DryRun: true, Logger: captureLogger{&stmts}})
	if err := session.Migrator().CreateTable(model.All()...); err != nil {
		return "", fmt.Errorf("render schema: %w", err)
	}
	return formatDDL(stmts), nil
}

// Diff loads currentSchemaSQL onto the writable shadow database, runs
// AutoMigrate(model.All()) for real, and returns the incremental DDL it issued
// to bring the current schema up to the models. The shadow's model tables are
// dropped first so a reused shadow doesn't pollute the result.
//
// A real (non-DryRun) execution on a throwaway shadow is used deliberately:
// DryRun AutoMigrate can panic in driver-specific alter paths (e.g. sqlite's
// recreate-table), whereas a real run on the shadow is exactly what a real
// apply would do, and the logger captures each statement.
func Diff(shadow *gorm.DB, currentSchemaSQL string) (string, error) {
	if err := ResetShadow(shadow); err != nil {
		return "", err
	}
	for _, stmt := range SplitStatements(currentSchemaSQL) {
		// Only replay schema DDL. A real pg_dump/mysqldump also carries session
		// setup (SET ..., SELECT pg_catalog.set_config('search_path','',...))
		// that is irrelevant to the schema shape and, in pg_dump's case,
		// actively breaks a later unqualified CREATE by blanking search_path.
		if !isDDL(stmt) {
			continue
		}
		if err := shadow.Exec(stmt).Error; err != nil {
			return "", fmt.Errorf("load current schema into shadow: %w", err)
		}
	}
	var stmts []string
	session := shadow.Session(&gorm.Session{Logger: captureLogger{&stmts}})
	if err := session.Migrator().AutoMigrate(model.All()...); err != nil {
		return "", fmt.Errorf("compute diff on shadow: %w", err)
	}
	return formatDDL(stmts), nil
}

// IntrospectSchema builds a best-effort CREATE TABLE script for the
// model.All() tables that currently exist on target, from GORM column
// introspection. Used by `nyro migrate diff --target-dsn` to seed the shadow
// with the target's current state.
//
// It is deliberately lossy: it reconstructs columns (name, type, nullability)
// but NOT indexes, constraints, or defaults — so a diff computed against it may
// re-suggest indexes/constraints that already exist on the target (review and
// skip those). Prefer `--target-file` with an exact schema dump when precision
// matters. target is only read (introspection SELECTs), never written.
func IntrospectSchema(target *gorm.DB) (string, error) {
	m := target.Migrator()
	quote := func(s string) string {
		st := &gorm.Statement{DB: target}
		return st.Quote(s)
	}
	var out []string
	for _, mdl := range model.All() {
		st := &gorm.Statement{DB: target}
		if err := st.Parse(mdl); err != nil {
			return "", fmt.Errorf("parse model %T: %w", mdl, err)
		}
		table := st.Table
		if !m.HasTable(mdl) {
			continue // absent on target → AutoMigrate will create it
		}
		cols, err := m.ColumnTypes(mdl)
		if err != nil {
			return "", fmt.Errorf("introspect %s: %w", table, err)
		}
		var defs, pk []string
		for _, c := range cols {
			typ := c.DatabaseTypeName()
			if ct, ok := c.ColumnType(); ok && ct != "" {
				typ = ct
			}
			nullable := ""
			if n, ok := c.Nullable(); ok && !n {
				nullable = " NOT NULL"
			}
			defs = append(defs, fmt.Sprintf("%s %s%s", quote(c.Name()), typ, nullable))
			if isPK, ok := c.PrimaryKey(); ok && isPK {
				pk = append(pk, quote(c.Name()))
			}
		}
		// Capture the primary key (the one constraint whose absence forces a
		// full table recreate on the diff). Indexes/other constraints remain
		// lossy — see the doc comment.
		if len(pk) > 0 {
			defs = append(defs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pk, ", ")))
		}
		out = append(out, fmt.Sprintf("CREATE TABLE %s (%s);", quote(table), strings.Join(defs, ", ")))
	}
	return strings.Join(out, "\n"), nil
}

// ResetShadow drops every model.All() table from db (reverse order for FK
// safety) so a reused shadow database starts clean.
func ResetShadow(db *gorm.DB) error {
	m := db.Migrator()
	all := model.All()
	for i := len(all) - 1; i >= 0; i-- {
		if m.HasTable(all[i]) {
			if err := m.DropTable(all[i]); err != nil {
				return fmt.Errorf("reset shadow: %w", err)
			}
		}
	}
	return nil
}

// SplitStatements breaks a SQL script into individual statements: it strips
// full-line "--" comments and psql "\" meta-commands (e.g. the \restrict /
// \unrestrict markers pg_dump emits), then splits on ";". Sufficient for schema
// DDL from a dump/export (no semicolons inside string literals, no procedure
// bodies) — it accepts both `nyro migrate dump` output and a real
// pg_dump/mysqldump schema-only dump.
func SplitStatements(sqlScript string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sqlScript, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, `\`) {
			continue // SQL comment or psql meta-command
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, part := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// formatDDL keeps only DDL statements (CREATE/ALTER/DROP), trims them, and
// joins them into a runnable script. Introspection SELECTs and other noise the
// logger captured are dropped.
func formatDDL(stmts []string) string {
	var out []string
	for _, s := range stmts {
		if isDDL(s) {
			out = append(out, strings.TrimSpace(s)+";")
		}
	}
	return strings.Join(out, "\n")
}

// isDDL reports whether s is a schema DDL statement (CREATE/ALTER/DROP),
// filtering out introspection SELECTs, session SET commands, and blank lines.
func isDDL(s string) bool {
	upper := strings.ToUpper(strings.TrimSpace(s))
	return strings.HasPrefix(upper, "CREATE") || strings.HasPrefix(upper, "ALTER") || strings.HasPrefix(upper, "DROP")
}
