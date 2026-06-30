package observability

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

func int32p(v int32) *int32 { return &v }

// writeLogs writes rows via a parquet.Sink and flushes them so Query can read.
func writeLogs(t *testing.T, dir string, rows []LogRecord) {
	t.Helper()
	s, err := parquet.NewSink[LogRecord](dir, "logs", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLogsQueryFiltersSortsPaginates(t *testing.T) {
	dir := t.TempDir()
	writeLogs(t, dir, []LogRecord{
		{ID: "old", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: int32p(200), CreatedAt: 1000},
		{ID: "err", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: int32p(500), CreatedAt: 2000},
		{ID: "other", ProviderID: "openai", ModelID: "gpt", ClientStatusCode: int32p(200), CreatedAt: 3000},
		{ID: "new", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: int32p(200), CreatedAt: 4000},
	})
	logs := NewLogs(dir)

	// No filters: all 4, sorted by CreatedAt desc.
	page, err := logs.Query(LogQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 {
		t.Fatalf("total: want 4, got %d", page.Total)
	}
	if len(page.Items) != 4 {
		t.Fatalf("items: want 4, got %d", len(page.Items))
	}
	if page.Items[0].ID != "new" || page.Items[3].ID != "old" {
		t.Errorf("expected newest-first [new..old], got [%s..%s]", page.Items[0].ID, page.Items[3].ID)
	}

	// Filter by provider.
	page, _ = logs.Query(LogQuery{Provider: "openai"})
	if page.Total != 1 || page.Items[0].ID != "other" {
		t.Errorf("provider filter: want 1=other, got total=%d first=%s", page.Total, firstID(page.Items))
	}

	// Filter by status max (>=500).
	page, _ = logs.Query(LogQuery{StatusMax: int32p(499)})
	if page.Total != 3 {
		t.Errorf("status<=499: want 3, got %d", page.Total)
	}
	for _, r := range page.Items {
		if r.ID == "err" {
			t.Errorf("err row should have been filtered out by StatusMax=499")
		}
	}

	// Pagination: limit=1 offset=1. Sorted desc by CreatedAt:
	// [new(4000), other(3000), err(2000), old(1000)] → page[1:2] = other.
	page, _ = logs.Query(LogQuery{Limit: 1, Offset: 1})
	if page.Total != 4 || len(page.Items) != 1 || page.Items[0].ID != "other" {
		t.Errorf("page[1:2] of sorted desc [new,other,err,old]: want other, got total=%d items=%v", page.Total, ids(page.Items))
	}

	// Default limit when Limit<=0 applied: with offset=0 returns up to 50, all 4 here.
	page, _ = logs.Query(LogQuery{})
	if page.Total != 4 || len(page.Items) != 4 {
		t.Errorf("default-limit query: want 4/4, got %d/%d", len(page.Items), page.Total)
	}

	// Offset beyond total → empty slice, total preserved.
	page, _ = logs.Query(LogQuery{Limit: 10, Offset: 100})
	if page.Total != 4 || len(page.Items) != 0 {
		t.Errorf("offset>total: want 0 items/total 4, got %d items/total %d", len(page.Items), page.Total)
	}
}

// TestLogsQueryStatusNilExcluded is a regression test for the contract that
// rows with a nil ClientStatusCode must be EXCLUDED when StatusMin/StatusMax is
// set (mirrors the legacy memory + sqlite backends, where NULL >= x is unknown).
func TestLogsQueryStatusNilExcluded(t *testing.T) {
	dir := t.TempDir()
	writeLogs(t, dir, []LogRecord{
		{ID: "nil-status", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: nil, CreatedAt: 1000},
		{ID: "ok-200", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: int32p(200), CreatedAt: 2000},
		{ID: "err-500", ProviderID: "anthropic", ModelID: "claude", ClientStatusCode: int32p(500), CreatedAt: 3000},
	})
	logs := NewLogs(dir)

	// StatusMin set: nil-status row must be excluded; matching 200/500 kept.
	page, err := logs.Query(LogQuery{StatusMin: int32p(200), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("StatusMin=200: want total 2 (nil excluded), got %d", page.Total)
	}
	for _, r := range page.Items {
		if r.ID == "nil-status" {
			t.Errorf("StatusMin=200: nil-status row should be EXCLUDED, but it appeared in results")
		}
	}
	got := map[string]bool{}
	for _, r := range page.Items {
		got[r.ID] = true
	}
	if !got["ok-200"] || !got["err-500"] {
		t.Errorf("StatusMin=200: want ok-200 and err-500 present, got %v", got)
	}

	// StatusMax set: nil-status row must be excluded; matching 200 kept.
	page, _ = logs.Query(LogQuery{StatusMax: int32p(499), Limit: 100})
	if page.Total != 1 {
		t.Fatalf("StatusMax=499: want total 1 (nil + 500 excluded), got %d", page.Total)
	}
	if page.Items[0].ID != "ok-200" {
		t.Errorf("StatusMax=499: want ok-200, got %s", page.Items[0].ID)
	}

	// No status filter: nil-status row is kept (only filtered by status bounds).
	page, _ = logs.Query(LogQuery{Limit: 100})
	if page.Total != 3 {
		t.Errorf("no status filter: want total 3 (nil kept), got %d", page.Total)
	}
}

// TestLogsQueryNegativeOffset is a regression test: a negative Offset must be
// clamped to 0 rather than panicking on filtered[start:end].
func TestLogsQueryNegativeOffset(t *testing.T) {
	dir := t.TempDir()
	writeLogs(t, dir, []LogRecord{
		{ID: "a", ProviderID: "anthropic", CreatedAt: 1000},
		{ID: "b", ProviderID: "anthropic", CreatedAt: 2000},
	})
	logs := NewLogs(dir)

	page, err := logs.Query(LogQuery{Limit: 10, Offset: -5})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("negative offset: want total 2, got %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("negative offset clamped to 0: want 2 items, got %d", len(page.Items))
	}
	// Sorted desc by CreatedAt: b(2000), a(1000).
	if page.Items[0].ID != "b" || page.Items[1].ID != "a" {
		t.Errorf("negative offset: want [b,a], got %v", ids(page.Items))
	}
}

func TestLogsFindByID(t *testing.T) {
	dir := t.TempDir()
	writeLogs(t, dir, []LogRecord{
		{ID: "alpha", ProviderID: "anthropic"},
		{ID: "beta", ProviderID: "openai"},
	})
	logs := NewLogs(dir)

	got, err := logs.FindByID("beta")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "beta" {
		t.Errorf("FindByID(beta): want beta, got %+v", got)
	}

	miss, err := logs.FindByID("nope")
	if err != nil {
		t.Fatal(err)
	}
	if miss != nil {
		t.Errorf("FindByID(nope): want nil, got %+v", miss)
	}
}

func TestLogsClearAll(t *testing.T) {
	dir := t.TempDir()
	writeLogs(t, dir, []LogRecord{{ID: "a"}, {ID: "b"}})

	// Sanity: files exist before.
	before, _ := parquet.CountFiles(dir, "logs")
	if before == 0 {
		t.Fatal("expected files to exist before ClearAll")
	}

	logs := NewLogs(dir)
	n, err := logs.ClearAll()
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("ClearAll removed count: want >=1, got %d", n)
	}
	after, _ := parquet.CountFiles(dir, "logs")
	if after != 0 {
		t.Errorf("ClearAll left %d files", after)
	}
}

func ids(rs []LogRecord) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func firstID(rs []LogRecord) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0].ID
}
