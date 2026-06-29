package memory

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/storage"
)

type logStore struct{ b *Backend }

func (s logStore) AppendBatch(entries []storage.RequestLog) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.logs = append(s.b.logs, entries...)
	return nil
}

func (s logStore) Query(q storage.LogQuery) (storage.LogPage, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	var filtered []storage.RequestLog
	for _, l := range s.b.logs {
		if q.Provider != "" && l.ProviderID != q.Provider {
			continue
		}
		if q.Model != "" && l.ModelID != q.Model {
			continue
		}
		if q.StatusMin != nil && (l.ClientStatusCode == nil || *l.ClientStatusCode < *q.StatusMin) {
			continue
		}
		if q.StatusMax != nil && (l.ClientStatusCode == nil || *l.ClientStatusCode > *q.StatusMax) {
			continue
		}
		filtered = append(filtered, l)
	}
	// newest first
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt > filtered[j].CreatedAt })

	total := int64(len(filtered))
	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := storage.LogPage{Total: total}
	if offset < end {
		page.Items = filtered[offset:end]
	}
	return page, nil
}

func (s logStore) FindByID(id string) (*storage.RequestLog, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for i := range s.b.logs {
		if s.b.logs[i].ID == id {
			l := s.b.logs[i]
			return &l, nil
		}
	}
	return nil, nil
}

func (s logStore) ClearAll() (int64, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	n := int64(len(s.b.logs))
	s.b.logs = nil
	return n, nil
}

func (s logStore) StatsOverview() (storage.StatsOverview, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	var st storage.StatsOverview
	var latencySum float64
	var latencyCount int64
	for _, l := range s.b.logs {
		st.TotalRequests++
		st.TotalInputTokens += int64(l.InputTokens)
		st.TotalOutputTokens += int64(l.OutputTokens)
		if l.ClientStatusCode != nil && *l.ClientStatusCode >= 400 {
			st.ErrorCount++
		}
		if l.LatencyTotalMs != nil {
			latencySum += float64(*l.LatencyTotalMs)
			latencyCount++
		}
	}
	if latencyCount > 0 {
		st.AvgDurationMs = latencySum / float64(latencyCount)
	}
	return st, nil
}
