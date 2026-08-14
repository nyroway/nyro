package redis

import (
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/quota"
)

func TestUsageKeyEscapesConsumerInsideHashTag(t *testing.T) {
	got := usageKey("consumer}:other", "requests", "m", 42)
	if strings.Contains(got, "{consumer}") || !strings.HasPrefix(got, "nyro:quota:v1:{") {
		t.Fatalf("unsafe key %q", got)
	}
	if strings.Contains(got, "consumer") || strings.Count(got, "{") != 1 || strings.Count(got, "}") != 1 {
		t.Fatalf("consumer escaped key = %q", got)
	}
}

func TestUsageBucketsCoverEveryMinuteOnceAndStayBounded(t *testing.T) {
	for minutes := 1; minutes <= int(quota.MaxWindow/time.Minute); minutes++ {
		for offset := 0; offset < 60; offset++ {
			current := int64(50_000 + offset)
			buckets := usageBuckets(current, time.Duration(minutes)*time.Minute)
			if len(buckets) > 83 {
				t.Fatalf("minutes=%d offset=%d returned %d buckets", minutes, offset, len(buckets))
			}
			start := current - int64(minutes) + 1
			covered := make(map[int64]int64)
			for _, bucket := range buckets {
				switch bucket.unit {
				case "m":
					covered[bucket.epoch] += bucket.coefficient
				case "h":
					for minute := bucket.epoch * 60; minute < bucket.epoch*60+60; minute++ {
						covered[minute] += bucket.coefficient
					}
				default:
					t.Fatalf("unknown bucket unit %q", bucket.unit)
				}
			}
			firstHourMinute := (start / 60) * 60
			lastHourMinute := (current/60)*60 + 59
			for minute := firstHourMinute; minute <= lastHourMinute; minute++ {
				want := int64(0)
				if minute >= start && minute <= current {
					want = 1
				}
				if covered[minute] != want {
					t.Fatalf("minutes=%d offset=%d coverage[%d]=%d, want %d", minutes, offset, minute, covered[minute], want)
				}
			}
		}
	}
}

func TestUsageBucketsClampWindowLikeMemory(t *testing.T) {
	const current = int64(100_000)
	subMinute := usageBuckets(current, time.Second)
	if len(subMinute) != 1 || subMinute[0] != (usageBucket{unit: "m", epoch: current, coefficient: 1}) {
		t.Fatalf("sub-minute buckets = %#v", subMinute)
	}

	overMaximum := usageBuckets(current, 48*time.Hour)
	maximum := usageBuckets(current, quota.MaxWindow)
	if len(overMaximum) != len(maximum) {
		t.Fatalf("over-max bucket count = %d, want %d", len(overMaximum), len(maximum))
	}
	for i := range maximum {
		if overMaximum[i] != maximum[i] {
			t.Fatalf("over-max bucket %d = %#v, want %#v", i, overMaximum[i], maximum[i])
		}
	}
}
