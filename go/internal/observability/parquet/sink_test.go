package parquet_test

import (
	"path/filepath"
	"testing"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

func TestSinkWriteRotateReadBack(t *testing.T) {
	dir := t.TempDir()
	s, err := parquet.NewSink[observability.LogRecord](dir, "logs", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// maxRows=2 → writing 3 rows forces a rotation (2 files).
	if err := s.Write([]observability.LogRecord{
		{ID: "a", ModelName: "m1"}, {ID: "b", ModelName: "m1"}, {ID: "c", ModelName: "m1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := filepath.Glob(filepath.Join(dir, "logs", "*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected >=2 rotated files, got %d (%v)", len(got), got)
	}
	// No .tmp files left behind (atomic rename).
	tmp, _ := filepath.Glob(filepath.Join(dir, "logs", "*.tmp"))
	if len(tmp) != 0 {
		t.Errorf("leftover .tmp files: %v", tmp)
	}
}
