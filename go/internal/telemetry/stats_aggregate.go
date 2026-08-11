package telemetry

import (
	"encoding/json"
	"log"
	"sort"
	"time"
)

type metricLabels struct {
	RouteID            string `json:"nyro.route.id"`
	RouteModel         string `json:"nyro.route.model"`
	UpstreamID         string `json:"nyro.upstream.id"`
	UpstreamName       string `json:"nyro.upstream.name"`
	ConsumerID         string `json:"nyro.consumer.id"`
	ResponseStatusCode int64  `json:"http.response.status_code"`
	Direction          string `json:"direction"`
}

// parseLabels decodes a labels_json blob. Malformed input is logged and yields
// zero-value labels (sample still counted, but unlabelled) rather than dropped.
func parseLabels(s string) metricLabels {
	var l metricLabels
	if s == "" {
		return l
	}
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		log.Printf("observability: malformed labels_json %q: %v", s, err)
	}
	return l
}

// AggregateStats rolls up delta metric samples into the four real-time stat
// shapes. The caller is responsible for selecting the requested time window.
func AggregateStats(samples []MetricSample, _ int64) (StatsOverview, []RouteStats, []UpstreamStats, []ConsumerStats, error) {
	var ov StatsOverview
	type histogramAcc struct {
		bounds     []float64
		buckets    []uint64
		compatible bool
	}
	addHistogram := func(acc *histogramAcc, sample MetricSample) {
		if len(sample.HistBuckets) != len(sample.HistBounds)+1 || len(sample.HistBuckets) == 0 {
			return
		}
		if acc.bounds == nil {
			acc.bounds = append([]float64(nil), sample.HistBounds...)
			acc.buckets = append([]uint64(nil), sample.HistBuckets...)
			acc.compatible = true
			return
		}
		if !acc.compatible || len(acc.bounds) != len(sample.HistBounds) {
			acc.compatible = false
			return
		}
		for i := range acc.bounds {
			if acc.bounds[i] != sample.HistBounds[i] {
				acc.compatible = false
				return
			}
		}
		for i := range acc.buckets {
			acc.buckets[i] += sample.HistBuckets[i]
		}
	}
	p95 := func(acc histogramAcc) *float64 {
		if !acc.compatible || len(acc.buckets) == 0 {
			return nil
		}
		var total uint64
		for _, count := range acc.buckets {
			total += count
		}
		if total == 0 {
			return nil
		}
		rank := (total*95 + 99) / 100
		var cumulative uint64
		for i, count := range acc.buckets {
			cumulative += count
			if cumulative < rank {
				continue
			}
			if i >= len(acc.bounds) {
				return nil
			}
			value := acc.bounds[i]
			return &value
		}
		return nil
	}
	var overviewHistogram histogramAcc
	type mAcc struct {
		req, in, out int64
		err          int64
		lat          time.Duration
		latCnt       int64
		hist         histogramAcc
	}
	type pAcc struct {
		req, err int64
		lat      time.Duration
		latCnt   int64
		hist     histogramAcc
	}
	type kAcc struct {
		req, in, out, cache int64
		lastTs              int64
	}
	routes := map[string]*mAcc{}
	upstreams := map[string]*pAcc{}
	consumers := map[string]*kAcc{}
	routeModels := map[string]string{}
	upstreamNames := map[string]string{}

	var latSum time.Duration
	var latCnt int64
	for _, s := range samples {
		l := parseLabels(s.LabelsJSON)
		switch s.Name {
		case "nyro_requests_total":
			value := int64(s.Value)
			ov.TotalRequests += value
			if l.ResponseStatusCode >= 400 {
				ov.ErrorCount += value
			}
			if l.RouteID != "" {
				mm := routes[l.RouteID]
				if mm == nil {
					mm = &mAcc{}
					routes[l.RouteID] = mm
				}
				routeModels[l.RouteID] = l.RouteModel
				mm.req += value
				if l.ResponseStatusCode >= 400 {
					mm.err += value
				}
			}
			if l.UpstreamID != "" {
				pp := upstreams[l.UpstreamID]
				if pp == nil {
					pp = &pAcc{}
					upstreams[l.UpstreamID] = pp
				}
				upstreamNames[l.UpstreamID] = l.UpstreamName
				pp.req += value
				if l.ResponseStatusCode >= 400 {
					pp.err += value
				}
			}
			if l.ConsumerID != "" {
				kk := consumers[l.ConsumerID]
				if kk == nil {
					kk = &kAcc{}
					consumers[l.ConsumerID] = kk
				}
				kk.req += value
				if s.Ts > kk.lastTs {
					kk.lastTs = s.Ts
				}
			}
		case "nyro_tokens_total":
			var kk *kAcc
			if l.ConsumerID != "" {
				kk = consumers[l.ConsumerID]
				if kk == nil {
					kk = &kAcc{}
					consumers[l.ConsumerID] = kk
				}
			}
			var mm *mAcc
			if l.RouteID != "" {
				mm = routes[l.RouteID]
				if mm == nil {
					mm = &mAcc{}
					routes[l.RouteID] = mm
				}
				routeModels[l.RouteID] = l.RouteModel
			}
			switch l.Direction {
			case "in":
				ov.TotalInputTokens += int64(s.Value)
				if mm != nil {
					mm.in += int64(s.Value)
				}
				if kk != nil {
					kk.in += int64(s.Value)
				}
			case "out":
				ov.TotalOutputTokens += int64(s.Value)
				if mm != nil {
					mm.out += int64(s.Value)
				}
				if kk != nil {
					kk.out += int64(s.Value)
				}
			case "cache_read":
				if kk != nil {
					kk.cache += int64(s.Value)
				}
			}
		case "nyro_request_latency_ms":
			addHistogram(&overviewHistogram, s)
			latSum += time.Duration(s.HistSum * float64(time.Millisecond))
			latCnt += s.HistCount
			if l.RouteID != "" {
				mm := routes[l.RouteID]
				if mm == nil {
					mm = &mAcc{}
					routes[l.RouteID] = mm
				}
				routeModels[l.RouteID] = l.RouteModel
				mm.lat += time.Duration(s.HistSum * float64(time.Millisecond))
				mm.latCnt += s.HistCount
				addHistogram(&mm.hist, s)
			}
			if l.UpstreamID != "" {
				pp := upstreams[l.UpstreamID]
				if pp == nil {
					pp = &pAcc{}
					upstreams[l.UpstreamID] = pp
				}
				upstreamNames[l.UpstreamID] = l.UpstreamName
				pp.lat += time.Duration(s.HistSum * float64(time.Millisecond))
				pp.latCnt += s.HistCount
				addHistogram(&pp.hist, s)
			}
		}
	}
	if latCnt > 0 {
		ov.AvgDurationMs = float64(latSum/time.Millisecond) / float64(latCnt)
	}
	ov.P95DurationMs = p95(overviewHistogram)

	models := make([]RouteStats, 0, len(routes))
	for id, a := range routes {
		avg := float64(0)
		if a.latCnt > 0 {
			avg = float64(a.lat.Milliseconds()) / float64(a.latCnt)
		}
		models = append(models, RouteStats{
			RouteID: id, RouteModel: routeModels[id], RequestCount: a.req, TotalInputTokens: a.in, TotalOutputTokens: a.out, AvgDurationMs: avg, P95DurationMs: p95(a.hist), ErrorCount: a.err,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].RequestCount != models[j].RequestCount {
			return models[i].RequestCount > models[j].RequestCount
		}
		return models[i].RouteID < models[j].RouteID
	})

	provs := make([]UpstreamStats, 0, len(upstreams))
	for id, a := range upstreams {
		avg := float64(0)
		if a.latCnt > 0 {
			avg = float64(a.lat.Milliseconds()) / float64(a.latCnt)
		}
		provs = append(provs, UpstreamStats{UpstreamID: id, UpstreamName: upstreamNames[id], RequestCount: a.req, ErrorCount: a.err, AvgDurationMs: avg, P95DurationMs: p95(a.hist)})
	}
	sort.Slice(provs, func(i, j int) bool {
		if provs[i].RequestCount != provs[j].RequestCount {
			return provs[i].RequestCount > provs[j].RequestCount
		}
		return provs[i].UpstreamID < provs[j].UpstreamID
	})

	keys := make([]ConsumerStats, 0, len(consumers))
	for id, a := range consumers {
		keys = append(keys, ConsumerStats{
			ConsumerID: id, RequestCount: a.req,
			TotalInputTokens: a.in, TotalOutputTokens: a.out, CacheReadTokens: a.cache, LastUsedAt: a.lastTs,
		})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].RequestCount != keys[j].RequestCount {
			return keys[i].RequestCount > keys[j].RequestCount
		}
		return keys[i].ConsumerID < keys[j].ConsumerID
	})

	return ov, models, provs, keys, nil
}

// AggregateHourly buckets samples into UTC hour buckets (ISO hour label).
func AggregateHourly(samples []MetricSample, _ int64) ([]StatsHourly, error) {
	type b struct {
		req, err, in, out int64
		latSum            float64
		latCnt            int64
	}
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
			if l.ResponseStatusCode >= 400 {
				bb.err += int64(s.Value)
			}
		case "nyro_tokens_total":
			switch l.Direction {
			case "in":
				bb.in += int64(s.Value)
			case "out":
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
		out = append(out, StatsHourly{
			Hour: hour, RequestCount: bb.req, ErrorCount: bb.err,
			TotalInputTokens: bb.in, TotalOutputTokens: bb.out, AvgDurationMs: avg,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}
