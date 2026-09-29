package cmdsession

import (
	"testing"
	"time"
)

func TestActivityFormatting(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "zero time", at: time.Time{}, want: ""},
		{name: "seconds ago", at: now.Add(-30 * time.Second), want: "now"},
		{name: "minutes ago", at: now.Add(-15 * time.Minute), want: "15m"},
		{name: "hours ago", at: now.Add(-4 * time.Hour), want: "4h"},
		{name: "days ago", at: now.Add(-3 * 24 * time.Hour), want: "3d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatActivityAt(tc.at, now)
			if got != tc.want {
				t.Fatalf("formatActivityAt(%v) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}
