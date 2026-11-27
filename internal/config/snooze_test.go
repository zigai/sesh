package config_test

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
)

func TestSnoozeUntilKeepsLocalWallClock(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Ljubljana")
	if err != nil {
		t.Fatal(err)
	}

	at := func(year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, loc)
	}

	tests := []struct {
		name   string
		choice string
		now    time.Time
		want   time.Time
	}{
		{
			name:   "clock on spring forward day",
			choice: "09:00",
			now:    at(2026, time.March, 29, 1, 0),
			want:   at(2026, time.March, 29, 9, 0),
		},
		{
			name:   "tomorrow from spring forward day",
			choice: "tomorrow 09:00",
			now:    at(2026, time.March, 29, 1, 0),
			want:   at(2026, time.March, 30, 9, 0),
		},
		{
			name:   "clock on fall back day",
			choice: "09:00",
			now:    at(2026, time.October, 25, 1, 0),
			want:   at(2026, time.October, 25, 9, 0),
		},
		{
			name:   "tomorrow from fall back day",
			choice: "tomorrow 09:00",
			now:    at(2026, time.October, 25, 1, 0),
			want:   at(2026, time.October, 26, 9, 0),
		},
		{
			name:   "passed clock rolls into spring forward day",
			choice: "09:00",
			now:    at(2026, time.March, 28, 22, 0),
			want:   at(2026, time.March, 29, 9, 0),
		},
		{
			name:   "tomorrow into spring forward day",
			choice: "tomorrow 09:00",
			now:    at(2026, time.March, 28, 22, 0),
			want:   at(2026, time.March, 29, 9, 0),
		},
		{
			name:   "clock later today",
			choice: "09:00",
			now:    at(2026, time.June, 10, 8, 0),
			want:   at(2026, time.June, 10, 9, 0),
		},
		{
			name:   "clock already passed today",
			choice: "09:00",
			now:    at(2026, time.June, 10, 10, 0),
			want:   at(2026, time.June, 11, 9, 0),
		},
		{
			name:   "clock exactly now",
			choice: "09:00",
			now:    at(2026, time.June, 10, 9, 0),
			want:   at(2026, time.June, 11, 9, 0),
		},
		{
			name:   "tomorrow on normal day",
			choice: "tomorrow 09:00",
			now:    at(2026, time.June, 10, 8, 0),
			want:   at(2026, time.June, 11, 9, 0),
		},
		{
			name:   "passed clock on new year's eve",
			choice: "09:00",
			now:    at(2026, time.December, 31, 23, 0),
			want:   at(2027, time.January, 1, 9, 0),
		},
		{
			name:   "tomorrow on new year's eve",
			choice: "tomorrow 09:00",
			now:    at(2026, time.December, 31, 23, 0),
			want:   at(2027, time.January, 1, 9, 0),
		},
		{
			name:   "duration across spring forward is elapsed time",
			choice: "1h",
			now:    at(2026, time.March, 29, 1, 30),
			want:   at(2026, time.March, 29, 3, 30),
		},
		{
			name:   "duration on normal day",
			choice: "1h",
			now:    at(2026, time.June, 10, 8, 15),
			want:   at(2026, time.June, 10, 9, 15),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snooze, err := config.ParseSnooze(tt.choice)
			if err != nil {
				t.Fatal(err)
			}

			if got := snooze.Until(tt.now); !got.Equal(tt.want) {
				t.Fatalf("Until(%s) = %s, want %s", tt.now, got, tt.want)
			}
		})
	}
}

func TestSnoozeUntilNextMessageIsZero(t *testing.T) {
	snooze, err := config.ParseSnooze(config.SnoozeNextMessage)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, time.March, 29, 1, 0, 0, 0, time.UTC)
	if got := snooze.Until(now); !got.IsZero() {
		t.Fatalf("Until(%s) = %s, want zero time", now, got)
	}
}
