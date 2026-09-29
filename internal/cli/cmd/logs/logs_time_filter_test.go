package cmdlogs

import (
	"testing"
	"time"
)

func TestParseTimeFilterTimezoneConsistency(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	now := time.Date(2026, 9, 10, 1, 0, 0, 0, tokyo)

	filter, err := parseTimeFilter("15:30", now)
	if err != nil {
		t.Fatal(err)
	}

	expected := time.Date(2026, 9, 9, 15, 30, 0, 0, time.UTC)
	if !filter.Equal(expected) {
		t.Fatalf("parseTimeFilter(15:30) = %s, want %s", filter, expected)
	}

	if filter.After(now) {
		t.Fatalf("parseTimeFilter(15:30) is in future: %s > %s", filter, now)
	}
}
