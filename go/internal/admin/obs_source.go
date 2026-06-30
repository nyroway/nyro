package admin

import (
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/storage"
)

// parquetLogSource is the LogSource backed by the parquet observability store
// (observability.Logs), with a fallback to the legacy request_logs table
// (storage.Storage.Logs) during the dual-write migration window.
//
// Read rule (the dual-write fallback):
//   - Query:     read parquet; if it has rows OR there is no fallback, return it.
//                 If parquet is empty AND a fallback exists, read the old table.
//   - FindByID:  try parquet; on miss, fall back to the old table.
//   - ClearAll:  clear BOTH stores (the WebUI "clear" button empties every copy
//                 of the data during migration). Returns the sum of removed rows.
//
// nil-safety: parquetLogSource always has a non-nil *observability.Logs (the
// constructor builds one from dir). fallback may be nil.
type parquetLogSource struct {
	logs     *observability.Logs
	fallback storage.Storage
}

// NewParquetLogSource builds a LogSource that reads parquet files under dir and
// falls back to the legacy table in st when parquet is empty. Pass a nil st to
// disable the fallback (used by tests that want to isolate the parquet path).
//
// Exported so cmd/admin can construct the dual-write read source without
// reaching into internal helpers.
func NewParquetLogSource(dir string, fallback storage.Storage) LogSource {
	return &parquetLogSource{logs: observability.NewLogs(dir), fallback: fallback}
}

// Query reads the parquet store. On a non-empty result (or when there is no
// fallback) it returns the parquet page; when parquet is empty and a fallback
// exists, it reads the old table instead.
func (p *parquetLogSource) Query(q storage.LogQuery) (storage.LogPage, error) {
	page, err := p.logs.Query(toObsQuery(q))
	if err != nil {
		return storage.LogPage{}, err
	}
	if len(page.Items) > 0 || p.fallback == nil {
		return toStoragePage(page), nil
	}
	return p.fallback.Logs().Query(q) // dual-write fallback
}

// FindByID tries parquet first; on a miss it falls back to the old table so a
// row written only to request_logs (during dual-write) is still found.
func (p *parquetLogSource) FindByID(id string) (*storage.RequestLog, error) {
	rec, err := p.logs.FindByID(id)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		rl := logRecordToRequestLog(*rec)
		return &rl, nil
	}
	if p.fallback == nil {
		return nil, nil
	}
	return p.fallback.Logs().FindByID(id)
}

// ClearAll empties BOTH stores during dual-write: parquet first (the new path),
// then the old request_logs table. The returned count is the sum of the two so
// the WebUI sees the total number of removed rows.
func (p *parquetLogSource) ClearAll() (int64, error) {
	n, err := p.logs.ClearAll()
	if err != nil {
		return 0, err
	}
	if p.fallback != nil {
		m, err := p.fallback.Logs().ClearAll()
		if err != nil {
			return 0, err
		}
		n += m
	}
	return n, nil
}

// queryLogs reads a page of logs. When src is non-nil it is the parquet-backed
// source (with old-table fallback); otherwise the legacy s.Logs() store is
// used. This is the read dispatch shared by GET /logs.
func queryLogs(src LogSource, s storage.Storage, q storage.LogQuery) (storage.LogPage, error) {
	if src != nil {
		return src.Query(q)
	}
	return s.Logs().Query(q)
}

// findLogByID looks up a single log row. When src is non-nil it is the
// parquet-backed source (which itself falls back to the old table on a miss);
// otherwise the legacy store is used. GET /logs/{id}.
func findLogByID(src LogSource, s storage.Storage, id string) (*storage.RequestLog, error) {
	if src != nil {
		return src.FindByID(id)
	}
	return s.Logs().FindByID(id)
}

// clearLogs empties the log store(s). When src is non-nil it clears BOTH the
// parquet store and the old table (the parquetLogSource ClearAll does both);
// otherwise it clears only the legacy store. DELETE /logs.
func clearLogs(src LogSource, s storage.Storage) (int64, error) {
	if src != nil {
		return src.ClearAll()
	}
	return s.Logs().ClearAll()
}

// toObsQuery converts the legacy storage.LogQuery into the observability
// facade's LogQuery (identical fields; the two structs mirror each other so the
// WebUI contract is unchanged).
func toObsQuery(q storage.LogQuery) observability.LogQuery {
	return observability.LogQuery{
		Limit:     q.Limit,
		Offset:    q.Offset,
		Provider:  q.Provider,
		Model:     q.Model,
		StatusMin: q.StatusMin,
		StatusMax: q.StatusMax,
	}
}

// toStoragePage converts an observability.LogPage into a storage.LogPage,
// mapping each LogRecord to a storage.RequestLog (field-for-field on the shared
// columns; columns the parquet schema does not capture stay zero-valued, so the
// JSON shape is preserved for the WebUI).
func toStoragePage(p observability.LogPage) storage.LogPage {
	items := make([]storage.RequestLog, len(p.Items))
	for i, r := range p.Items {
		items[i] = logRecordToRequestLog(r)
	}
	return storage.LogPage{Items: items, Total: p.Total}
}

// logRecordToRequestLog copies a parquet-backed LogRecord into the legacy
// storage.RequestLog shape. Every field present on LogRecord maps 1:1 onto the
// identically-named column of RequestLog; the header/body/stream-chunk columns
// that exist only on RequestLog are left zero (the parquet schema does not
// capture them).
func logRecordToRequestLog(r observability.LogRecord) storage.RequestLog {
	return storage.RequestLog{
		ID:                 r.ID,
		CreatedAt:          r.CreatedAt,
		APIKeyID:           r.APIKeyID,
		APIKeyName:         r.APIKeyName,
		ClientProtocol:     r.ClientProtocol,
		UpstreamProtocol:   r.UpstreamProtocol,
		ProviderID:         r.ProviderID,
		ProviderName:       r.ProviderName,
		ModelID:            r.ModelID,
		ModelName:          r.ModelName,
		ClientModel:        r.ClientModel,
		UpstreamModel:      r.UpstreamModel,
		Method:             r.Method,
		Path:               r.Path,
		ClientStatusCode:   r.ClientStatusCode,
		UpstreamStatusCode: r.UpstreamStatusCode,
		LatencyTotalMs:     r.LatencyTotalMs,
		LatencyUpstreamMs:  r.LatencyUpstreamMs,
		InputTokens:        r.InputTokens,
		OutputTokens:       r.OutputTokens,
		CacheReadTokens:    r.CacheReadTokens,
		IsStream:           r.IsStream,
	}
}
