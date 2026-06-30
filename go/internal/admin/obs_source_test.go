package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newParquetEngine mounts the admin API with a parquet-backed LogSource rooted
// at obsDir (and the old request_logs table as fallback). Mirrors newEngine but
// injects the real parquetLogSource so /logs reads parquet.
func newParquetEngine(t *testing.T, obsDir string) (chi.Router, *memory.Backend) {
	t.Helper()
	st := memory.New()
	r := chi.NewRouter()
	Mount(r, st.Storage(), "", NewParquetLogSource(obsDir, st.Storage()), nil)
	return r, st
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

// TestLogsParquetRead asserts that when parquet has a row, /logs returns it
// (the new parquet path, not the old table).
func TestLogsParquetRead(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newParquetEngine(t, obsDir)

	// Seed BOTH stores with different rows so we can prove parquet wins.
	flushLogs(t, obsDir, observability.LogRecord{
		ID: "pq-1", CreatedAt: 1_700_000_000_000, ProviderID: "p1", ModelID: "m1",
		ModelName: "parquet-row", ProviderName: "OpenAI", InputTokens: 11,
	})
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-1", CreatedAt: 1_700_000_000_001, ModelName: "old-row", ClientStatusCode: &code200},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(r, "GET", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/logs → %d %s", rec.Code, rec.Body.String())
	}
	var page storage.LogPage
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

// TestLogsFallbackWhenParquetEmpty asserts the dual-write fallback: with an
// empty parquet store, /logs reads the legacy request_logs table instead.
func TestLogsFallbackWhenParquetEmpty(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newParquetEngine(t, obsDir)

	// Seed ONLY the old table; parquet is empty.
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-1", CreatedAt: 1_700_000_000_000, ModelName: "fallback-row", ClientStatusCode: &code200, InputTokens: 7},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(r, "GET", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/logs → %d %s", rec.Code, rec.Body.String())
	}
	var page storage.LogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "old-1" {
		t.Fatalf("expected fallback row old-1, got %+v", page.Items)
	}
	if page.Items[0].InputTokens != 7 {
		t.Errorf("InputTokens mismatch: %d", page.Items[0].InputTokens)
	}
}

// TestLogsFindByIDParquetThenFallback asserts FindByID tries parquet first,
// then falls back to the old table for an id only it has.
func TestLogsFindByIDParquetThenFallback(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newParquetEngine(t, obsDir)

	flushLogs(t, obsDir, observability.LogRecord{ID: "pq-99", CreatedAt: 1, ModelName: "from-parquet"})
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-99", CreatedAt: 1, ModelName: "from-old", ClientStatusCode: &code200},
	}); err != nil {
		t.Fatal(err)
	}

	// parquet hit
	rec := do(r, "GET", "/api/v1/logs/pq-99", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("parquet find → %d %s", rec.Code, rec.Body.String())
	}
	var l storage.RequestLog
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.ID != "pq-99" || l.ModelName != "from-parquet" {
		t.Errorf("parquet row wrong: %+v", l)
	}

	// fallback hit (id only in old table)
	rec = do(r, "GET", "/api/v1/logs/old-99", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback find → %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.ID != "old-99" || l.ModelName != "from-old" {
		t.Errorf("fallback row wrong: %+v", l)
	}

	// miss in both
	rec = do(r, "GET", "/api/v1/logs/nope", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id → %d, want 404", rec.Code)
	}
}

// TestLogsClearAllClearsBoth asserts DELETE /logs empties BOTH parquet and the
// old request_logs table during the dual-write window.
func TestLogsClearAllClearsBoth(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newParquetEngine(t, obsDir)

	flushLogs(t, obsDir, observability.LogRecord{ID: "pq-1", CreatedAt: 1, ModelName: "pq"})
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-1", CreatedAt: 1, ModelName: "old", ClientStatusCode: &code200},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(r, "DELETE", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete → %d %s", rec.Code, rec.Body.String())
	}

	// Old table should now be empty.
	if page, _ := st.Logs().Query(storage.LogQuery{Limit: 100}); len(page.Items) != 0 {
		t.Errorf("old table not cleared: %+v", page.Items)
	}
	// Parquet store should now be empty too (files removed). The Logs facade
	// reads <dir>/logs, so pass obsDir (the same dir newParquetLogSource used).
	logs := observability.NewLogs(obsDir)
	pqPage, err := logs.Query(observability.LogQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(pqPage.Items) != 0 {
		t.Errorf("parquet not cleared: %+v", pqPage.Items)
	}
}
