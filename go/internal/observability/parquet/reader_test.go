package parquet_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

func TestReadSinceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := parquet.NewSink[observability.LogRecord](dir, "logs", 100)
	rows := []observability.LogRecord{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if err := s.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := parquet.ReadSince[observability.LogRecord](dir, "logs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	// IDs survive the parquet round-trip.
	want := map[string]bool{"a": false, "b": false, "c": false}
	for _, r := range got {
		want[r.ID] = true
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing id %q", k)
		}
	}
}
