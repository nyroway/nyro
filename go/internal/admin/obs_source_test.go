package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newParquetEngine mounts the admin API with a parquet-backed LogSource rooted
// at obsDir. Mirrors newEngine but injects the real parquetLogSource so /logs
// reads parquet.
func newParquetEngine(t *testing.T, obsDir string) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	// The storage backend is unused by /logs (parquet-only) but required by
	// Mount's signature.
	Mount(r, memory.New().Storage(), "", NewParquetLogSource(obsDir), nil)
	return r
}

// flushLogs writes one LogRecord into the parquet logs dir and flushes it so
// the reader can see it (the sink otherwise only flushes at maxRows/hour).
func flushLogs(t *testing.T, obsDir string, rows ...observability.LogRecord) {
	t.Helper()
	sink, err := parquet.NewSink[observability.LogRecord](obsDir, "logs", 50000)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := sink.Flush(); err != nil {
		t.Fatal(err)
	}
}

// TestLogsParquetRead asserts that when parquet has a row, /logs returns it.
func TestLogsParquetRead(t *testing.T) {
	obsDir := t.TempDir()
	r := newParquetEngine(t, obsDir)

	flushLogs(t, obsDir, observability.LogRecord{
		ID: "pq-1", CreatedAt: 1_700_000_000_000, ProviderID: "p1", ModelID: "m1",
		ModelName: "parquet-row", ProviderName: "OpenAI", InputTokens: 11,
	})

	rec := do(r, "GET", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/logs → %d %s", rec.Code, rec.Body.String())
	}
	var page observability.LogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "pq-1" {
		t.Fatalf("expected parquet row pq-1, got %+v", page.Items)
	}
	if page.Items[0].InputTokens != 11 {
		t.Errorf("InputTokens not copied: %d", page.Items[0].InputTokens)
	}
}

// TestLogsFindByID asserts the parquet path resolves a known id and 404s on a
// miss.
func TestLogsFindByID(t *testing.T) {
	obsDir := t.TempDir()
	r := newParquetEngine(t, obsDir)

	flushLogs(t, obsDir, observability.LogRecord{ID: "pq-99", CreatedAt: 1, ModelName: "from-parquet"})

	// hit
	rec := do(r, "GET", "/api/v1/logs/pq-99", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("find → %d %s", rec.Code, rec.Body.String())
	}
	var l observability.LogRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.ID != "pq-99" || l.ModelName != "from-parquet" {
		t.Errorf("row wrong: %+v", l)
	}

	// miss
	rec = do(r, "GET", "/api/v1/logs/nope", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id → %d, want 404", rec.Code)
	}
}

// TestLogsClearAll asserts DELETE /logs empties the parquet store.
func TestLogsClearAll(t *testing.T) {
	obsDir := t.TempDir()
	r := newParquetEngine(t, obsDir)

	flushLogs(t, obsDir, observability.LogRecord{ID: "pq-1", CreatedAt: 1, ModelName: "pq"})

	rec := do(r, "DELETE", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete → %d %s", rec.Code, rec.Body.String())
	}

	// Parquet store should now be empty (files removed).
	logs := observability.NewLogs(obsDir)
	pqPage, err := logs.Query(observability.LogQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(pqPage.Items) != 0 {
		t.Errorf("parquet not cleared: %+v", pqPage.Items)
	}
}
