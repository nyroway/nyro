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
