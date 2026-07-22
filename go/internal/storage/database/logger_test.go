package database

import (
	"bytes"
	"strings"
	"testing"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/storage/model"
)

// openWithCapturedLog opens an in-memory database whose GORM logger writes into
// the returned buffer, so tests can assert on what a deployment would actually
// find in its logs.
func openWithCapturedLog(t *testing.T) (*gorm.DB, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: newGormLogger(&logs)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.Upstream{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, &logs
}

// A missing row is a normal outcome in this package — settings.Get returns
// ("", nil) for an unset key — so it must not be logged. Before this, every
// boot emitted a dozen "record not found" SQL blocks describing nothing an
// operator could act on, which is exactly how people learn to ignore logs.
func TestGormLogger_DoesNotLogMissingRows(t *testing.T) {
	db, logs := openWithCapturedLog(t)

	var row model.Setting
	err := db.Where("key = ?", "obs_logs_exporter").First(&row).Error
	if err == nil {
		t.Fatal("expected gorm.ErrRecordNotFound from an empty table")
	}
	if got := logs.String(); got != "" {
		t.Errorf("a missing row must not be logged, got:\n%s", got)
	}
}

// Reading the log file must not be a way to obtain credentials. GORM's default
// logger interpolates parameters into the SQL it prints, so any slow or failing
// statement touching upstreams would write the provider key verbatim.
func TestGormLogger_DoesNotLogParameterValues(t *testing.T) {
	db, logs := openWithCapturedLog(t)

	const secret = `{"api_key":"sk-must-never-reach-the-log"}`
	// Insert once so the second insert violates the primary key and GORM logs
	// the failing statement — the realistic path by which a write's parameters
	// reach the log.
	up := &model.Upstream{ID: "up-1", Name: "openai", CredentialsJSON: secret}
	if err := db.Create(up).Error; err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := db.Create(up).Error; err == nil {
		t.Fatal("expected a duplicate-key error to force GORM to log the statement")
	}

	got := logs.String()
	if got == "" {
		t.Fatal("expected the failing statement to be logged at all")
	}
	if strings.Contains(got, "sk-must-never-reach-the-log") {
		t.Errorf("credential value leaked into the log:\n%s", got)
	}
	if !strings.Contains(got, "?") {
		t.Errorf("expected placeholders in the logged SQL, got:\n%s", got)
	}
}

// ANSI escapes are noise once logs are a file or journald rather than a
// terminal, which is the normal case for a long-running service.
func TestGormLogger_HasNoColorEscapes(t *testing.T) {
	db, logs := openWithCapturedLog(t)

	up := &model.Upstream{ID: "up-1", Name: "openai"}
	_ = db.Create(up).Error
	_ = db.Create(up).Error // duplicate, forces a log line

	if strings.Contains(logs.String(), "\033[") {
		t.Errorf("logger emitted ANSI colour escapes:\n%q", logs.String())
	}
}
