package observability

import (
	"encoding/json"
	"sort"
	"time"
)

type metricLabels struct {
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	APIKey      string `json:"apikey"`
	StatusClass string `json:"status_class"`
	Direction   string `json:"direction"`
}

func parseLabels(s string) metricLabels {
	var l metricLabels
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

// AggregateStats rolls up a slice of MetricSamples (a window of metric-history
// parquet rows) into the four real-time stat shapes. hours<=0 means "all".
func AggregateStats(samples []MetricSample, _ int64) (StatsOverview, []ModelStats, []ProviderStats, []ApiKeyStats, error) {
	var ov StatsOverview
	type mAcc struct{ req, in, out int64; lat time.Duration }
	type pAcc struct{ req, err int64; lat time.Duration }
	type kAcc struct{ name string; req, in, out, cache int64; lastTs int64 }
	mmodels := map[string]*mAcc{}
	mprov := map[string]*pAcc{}
	mkey := map[string]*kAcc{}

	var latSum time.Duration
	var latCnt int64
	for _, s := range samples {
		l := parseLabels(s.LabelsJSON)
		switch s.Name {
		case "nyro_requests_total":
			ov.TotalRequests += int64(s.Value)
			if l.StatusClass == "5xx" || l.StatusClass == "4xx" {
				ov.ErrorCount += int64(s.Value)
			}
			mm := mmodels[l.Model]
			if mm == nil {
				mm = &mAcc{}
				mmodels[l.Model] = mm
			}
			mm.req++
			pp := mprov[l.Provider]
			if pp == nil {
				pp = &pAcc{}
				mprov[l.Provider] = pp
			}
			pp.req++
			if l.StatusClass == "4xx" || l.StatusClass == "5xx" {
				pp.err++
			}
			kk := mkey[l.APIKey]
			if kk == nil {
				kk = &kAcc{name: l.APIKey}
				mkey[l.APIKey] = kk
			}
			kk.req++
			if s.Ts > kk.lastTs {
				kk.lastTs = s.Ts
			}
		case "nyro_tokens_total":
			kk := mkey[l.APIKey]
			if kk == nil {
				kk = &kAcc{name: l.APIKey}
				mkey[l.APIKey] = kk
			}
			mm := mmodels[l.Model]
			if mm == nil {
				mm = &mAcc{}
				mmodels[l.Model] = mm
			}
			switch l.Direction {
			case "in":
				ov.TotalInputTokens += int64(s.Value)
				mm.in += int64(s.Value)
				kk.in += int64(s.Value)
			case "out":
				ov.TotalOutputTokens += int64(s.Value)
				mm.out += int64(s.Value)
				kk.out += int64(s.Value)
			case "cache_read":
				kk.cache += int64(s.Value)
			}
		case "nyro_request_latency_ms":
			latSum += time.Duration(s.HistSum * float64(time.Millisecond))
			latCnt += s.HistCount
			if mm := mmodels[l.Model]; mm != nil {
				mm.lat += time.Duration(s.HistSum * float64(time.Millisecond))
			}
		}
	}
	if latCnt > 0 {
		ov.AvgDurationMs = float64(latSum/time.Millisecond) / float64(latCnt)
	}

	models := make([]ModelStats, 0, len(mmodels))
	for name, a := range mmodels {
		avg := float64(0)
		models = append(models, ModelStats{
			Model: name, RequestCount: a.req, TotalInputTokens: a.in, TotalOutputTokens: a.out, AvgDurationMs: avg,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].RequestCount > models[j].RequestCount })

	provs := make([]ProviderStats, 0, len(mprov))
	for name, a := range mprov {
		provs = append(provs, ProviderStats{Provider: name, RequestCount: a.req, ErrorCount: a.err})
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i].RequestCount > provs[j].RequestCount })

	keys := make([]ApiKeyStats, 0, len(mkey))
	for id, a := range mkey {
		name := id
		if a.name != "" {
			name = a.name
		}
		keys = append(keys, ApiKeyStats{APIKeyID: id, APIKeyName: name, RequestCount: a.req,
			TotalInputTokens: a.in, TotalOutputTokens: a.out, CacheReadTokens: a.cache, LastUsedAt: a.lastTs})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].RequestCount > keys[j].RequestCount })

	return ov, models, provs, keys, nil
}

// AggregateHourly buckets samples into UTC hour buckets (ISO hour label).
func AggregateHourly(samples []MetricSample, _ int64) ([]StatsHourly, error) {
	type b struct{ req, err, in, out int64; latSum float64; latCnt int64 }
	buckets := map[string]*b{}
	for _, s := range samples {
		hour := time.Unix(0, s.Ts).UTC().Truncate(time.Hour).Format("2006-01-02T15:00:00Z")
		bb := buckets[hour]
		if bb == nil {
			bb = &b{}
			buckets[hour] = bb
		}
		l := parseLabels(s.LabelsJSON)
		switch s.Name {
		case "nyro_requests_total":
			bb.req += int64(s.Value)
			if l.StatusClass == "4xx" || l.StatusClass == "5xx" {
				bb.err += int64(s.Value)
			}
		case "nyro_tokens_total":
			if l.Direction == "in" {
				bb.in += int64(s.Value)
			} else if l.Direction == "out" {
				bb.out += int64(s.Value)
			}
		case "nyro_request_latency_ms":
			bb.latSum += s.HistSum
			bb.latCnt += s.HistCount
		}
	}
	out := make([]StatsHourly, 0, len(buckets))
	for hour, bb := range buckets {
		avg := float64(0)
		if bb.latCnt > 0 {
			avg = bb.latSum / float64(bb.latCnt)
		}
		out = append(out, StatsHourly{Hour: hour, RequestCount: bb.req, ErrorCount: bb.err,
			TotalInputTokens: bb.in, TotalOutputTokens: bb.out, AvgDurationMs: avg})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}
