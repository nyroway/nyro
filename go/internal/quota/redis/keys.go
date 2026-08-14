package redis

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/nyroway/nyro/go/internal/quota"
)

const keyPrefix = "nyro:quota:v1"

type usageBucket struct {
	unit        string
	epoch       int64
	coefficient int64
}

type usageTerm struct {
	key         string
	coefficient int64
}

func encodedConsumer(consumerID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(consumerID))
}

func usageKey(consumerID, quotaType, unit string, epoch int64) string {
	return fmt.Sprintf("%s:{%s}:%s:%s:%d", keyPrefix, encodedConsumer(consumerID), quotaType, unit, epoch)
}

func concurrencyKey(consumerID string) string {
	return fmt.Sprintf("%s:{%s}:concurrency", keyPrefix, encodedConsumer(consumerID))
}

func usageBuckets(currentMinute int64, window time.Duration) []usageBucket {
	minutes := int64(window / quota.BucketResolution)
	if minutes < 1 {
		minutes = 1
	}
	maximum := int64(quota.MaxWindow / quota.BucketResolution)
	if minutes > maximum {
		minutes = maximum
	}

	start := currentMinute - minutes + 1
	buckets := make([]usageBucket, 0, 83)
	firstHour := floorHour(start)
	lastHour := floorHour(currentMinute)
	for hour := firstHour; hour <= lastHour; hour++ {
		hourStart := hour * 60
		includedStart := max(start, hourStart)
		includedEnd := min(currentMinute, hourStart+59)
		included := includedEnd - includedStart + 1
		if included == 60 {
			buckets = append(buckets, usageBucket{unit: "h", epoch: hour, coefficient: 1})
			continue
		}
		if included <= 30 {
			for minute := includedStart; minute <= includedEnd; minute++ {
				buckets = append(buckets, usageBucket{unit: "m", epoch: minute, coefficient: 1})
			}
			continue
		}
		buckets = append(buckets, usageBucket{unit: "h", epoch: hour, coefficient: 1})
		for minute := hourStart; minute < includedStart; minute++ {
			buckets = append(buckets, usageBucket{unit: "m", epoch: minute, coefficient: -1})
		}
		for minute := includedEnd + 1; minute < hourStart+60; minute++ {
			buckets = append(buckets, usageBucket{unit: "m", epoch: minute, coefficient: -1})
		}
	}
	return buckets
}

func usageTerms(consumerID, quotaType string, currentMinute int64, window time.Duration) []usageTerm {
	buckets := usageBuckets(currentMinute, window)
	terms := make([]usageTerm, len(buckets))
	for i, bucket := range buckets {
		terms[i] = usageTerm{
			key:         usageKey(consumerID, quotaType, bucket.unit, bucket.epoch),
			coefficient: bucket.coefficient,
		}
	}
	return terms
}

func floorHour(minute int64) int64 {
	if minute >= 0 {
		return minute / 60
	}
	return (minute - 59) / 60
}
