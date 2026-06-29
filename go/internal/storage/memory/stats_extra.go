package memory

import (
	"sort"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

func (s logStore) StatsByModel() ([]storage.ModelStats, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	type agg struct {
		cnt, in, out, errN, latN int64
		latSum                   float64
	}
	m := map[string]*agg{}
	for _, l := range s.b.logs {
		if l.ModelName == "" {
			continue
		}
		a := m[l.ModelName]
		if a == nil {
			a = &agg{}
			m[l.ModelName] = a
		}
		a.cnt++
		a.in += int64(l.InputTokens)
		a.out += int64(l.OutputTokens)
		if l.LatencyTotalMs != nil {
			a.latSum += float64(*l.LatencyTotalMs)
			a.latN++
		}
		if l.ClientStatusCode != nil && *l.ClientStatusCode >= 400 {
			a.errN++
		}
	}
	out := make([]storage.ModelStats, 0, len(m))
	for name, a := range m {
		out = append(out, storage.ModelStats{Model: name, RequestCount: a.cnt, TotalInputTokens: a.in, TotalOutputTokens: a.out, AvgDurationMs: avgMs(a.latSum, a.latN)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestCount > out[j].RequestCount })
	return out, nil
}

func (s logStore) StatsByProvider() ([]storage.ProviderStats, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	type agg struct {
		cnt, errN, latN int64
		latSum          float64
	}
	m := map[string]*agg{}
	for _, l := range s.b.logs {
		if l.ProviderName == "" {
			continue
		}
		a := m[l.ProviderName]
		if a == nil {
			a = &agg{}
			m[l.ProviderName] = a
		}
		a.cnt++
		if l.ClientStatusCode != nil && *l.ClientStatusCode >= 400 {
			a.errN++
		}
		if l.LatencyTotalMs != nil {
			a.latSum += float64(*l.LatencyTotalMs)
			a.latN++
		}
	}
	out := make([]storage.ProviderStats, 0, len(m))
	for name, a := range m {
		out = append(out, storage.ProviderStats{Provider: name, RequestCount: a.cnt, ErrorCount: a.errN, AvgDurationMs: avgMs(a.latSum, a.latN)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestCount > out[j].RequestCount })
	return out, nil
}

func (s logStore) StatsHourly(hours int64) ([]storage.StatsHourly, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	cutoff := time.Now().UnixMilli() - hours*3_600_000
	type agg struct {
		cnt, in, out, errN, latN int64
		latSum                   float64
	}
	m := map[int64]*agg{}
	for _, l := range s.b.logs {
		if l.CreatedAt < cutoff {
			continue
		}
		hour := (l.CreatedAt / 3_600_000) * 3_600_000
		a := m[hour]
		if a == nil {
			a = &agg{}
			m[hour] = a
		}
		a.cnt++
		a.in += int64(l.InputTokens)
		a.out += int64(l.OutputTokens)
		if l.ClientStatusCode != nil && *l.ClientStatusCode >= 400 {
			a.errN++
		}
		if l.LatencyTotalMs != nil {
			a.latSum += float64(*l.LatencyTotalMs)
			a.latN++
		}
	}
	out := make([]storage.StatsHourly, 0, len(m))
	for hour, a := range m {
		out = append(out, storage.StatsHourly{
			Hour:         time.UnixMilli(hour).UTC().Format("2006-01-02T15:00:00Z"),
			RequestCount: a.cnt, ErrorCount: a.errN,
			TotalInputTokens: a.in, TotalOutputTokens: a.out, AvgDurationMs: avgMs(a.latSum, a.latN),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}

func avgMs(sum float64, n int64) float64 {
	if n > 0 {
		return sum / float64(n)
	}
	return 0
}
