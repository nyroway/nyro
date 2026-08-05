package ctl

import (
	"testing"
	"time"
)

func TestTruncate(t *testing.T) {
	if got := truncate("abc123def456xyz", 12); got != "abc123def456" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("short", 12); got != "short" {
		t.Fatalf("truncate short = %q", got)
	}
}

func TestHumanizeTime(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5 minutes ago"},
		{now.Add(-1 * time.Minute), "1 minute ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-1 * time.Hour), "1 hour ago"},
		{now.Add(-2 * 24 * time.Hour), "2 days ago"},
		{now.Add(-1 * 24 * time.Hour), "1 day ago"},
		{now.Add(-60 * 24 * time.Hour), "2 months ago"},
		{now.Add(-35 * 24 * time.Hour), "1 month ago"},
	}
	for _, tc := range cases {
		got := humanizeTime(tc.at.Format(time.RFC3339))
		if got != tc.want {
			t.Fatalf("humanizeTime(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
	if got := humanizeTime(""); got != "-" {
		t.Fatalf("empty = %q", got)
	}
	if got := humanizeTime("not-a-time"); got != "not-a-time" {
		t.Fatalf("invalid = %q", got)
	}
}
