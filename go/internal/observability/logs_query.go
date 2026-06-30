package observability

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

// LogQuery filters and paginates a Logs.Query call. It mirrors the legacy
// storage.LogQuery shape so the WebUI contract is unchanged.
type LogQuery struct {
	Limit     int64
	Offset    int64
	Provider  string
	Model     string
	StatusMin *int32
	StatusMax *int32
}

// LogPage is the result of Logs.Query: the page of records plus the total
// number of matching records (ignoring pagination), so callers can render
// "showing 1–50 of 312".
type LogPage struct {
	Items []LogRecord `json:"items"`
	Total int64       `json:"total"`
}

// Logs is a read facade over the admin's parquet log store. Constructed by the
// admin process only.
type Logs struct{ dir string }

// NewLogs returns a Logs facade rooted at dir (the same dir a parquet.Sink
// writes into).
func NewLogs(dir string) *Logs { return &Logs{dir: dir} }

// Query reads every persisted log row, applies the provider/model/status
// filters, sorts by CreatedAt descending, then returns the requested page.
// Limit<=0 defaults to 50; Offset beyond the result set yields an empty page
// while Total still reports the full match count.
func (l *Logs) Query(q LogQuery) (LogPage, error) {
	rows, err := parquet.ReadSince[LogRecord](l.dir, "logs", 0)
	if err != nil {
		return LogPage{}, err
	}
	filtered := rows[:0]
	for _, r := range rows {
		if q.Provider != "" && r.ProviderID != q.Provider {
			continue
		}
		if q.Model != "" && r.ModelID != q.Model {
			continue
		}
		// Mirror the legacy memory/sqlite contract: when a status bound is set,
		// rows with a nil ClientStatusCode are EXCLUDED (NULL >= x is unknown in
		// SQL; the memory backend treats nil the same way).
		if q.StatusMin != nil && (r.ClientStatusCode == nil || *r.ClientStatusCode < *q.StatusMin) {
			continue
		}
		if q.StatusMax != nil && (r.ClientStatusCode == nil || *r.ClientStatusCode > *q.StatusMax) {
			continue
		}
		filtered = append(filtered, r)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt > filtered[j].CreatedAt })
	total := int64(len(filtered))
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	start := q.Offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return LogPage{Items: filtered[start:end], Total: total}, nil
}

// FindByID returns the first log row whose ID matches, or nil if none. It scans
// all persisted rows, so it is intended for low-volume lookups (UI drill-down),
// not bulk export.
func (l *Logs) FindByID(id string) (*LogRecord, error) {
	rows, err := parquet.ReadSince[LogRecord](l.dir, "logs", 0)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == id {
			rc := r
			return &rc, nil
		}
	}
	return nil, nil
}

// ClearAll deletes every persisted log file and returns the number of files
// removed.
func (l *Logs) ClearAll() (int64, error) {
	n, err := parquet.RemoveAll(l.dir, "logs")
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}
