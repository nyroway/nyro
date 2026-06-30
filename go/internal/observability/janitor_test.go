package observability

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanSignalRemovesOldKeepsNew(t *testing.T) {
	dir := t.TempDir()
	sigDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(sigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Old file: should be removed.
	oldPath := filepath.Join(sigDir, "2026010101-old.parquet")
	if err := os.WriteFile(oldPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().UTC().AddDate(0, 0, -10)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Fresh file: should survive.
	freshPath := filepath.Join(sigDir, "2026063015-fresh.parquet")
	if err := os.WriteFile(freshPath, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run cleanSignal with a 7-day retention window.
	cleanSignal(dir, "logs", 7, time.Now().UTC())

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expected old file removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("expected fresh file to survive, stat err=%v", err)
	}
}

func TestCleanSignalSkipsZeroDays(t *testing.T) {
	dir := t.TempDir()
	sigDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(sigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Even an "old" file is left untouched when days<=0 (signal disabled).
	p := filepath.Join(sigDir, "old.parquet")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -365)
	os.Chtimes(p, old, old)

	cleanSignal(dir, "logs", 0, time.Now().UTC())

	if _, err := os.Stat(p); err != nil {
		t.Errorf("days<=0 should skip signal, stat err=%v", err)
	}
}
