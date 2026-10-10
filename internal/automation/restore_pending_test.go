//go:build linux || darwin

package automation

import (
	"slices"
	"testing"
	"time"
)

func TestUnrestoredSessionsCountsSavedSessionsMissingFromANewerServer(t *testing.T) {
	t.Parallel()

	saved := time.Unix(1_000, 0)
	started := time.Unix(2_000, 0)

	if got := UnrestoredSessions(started, saved, []string{"0", "work", "agent"}, []string{"0"}); !slices.Equal(got, []string{"work", "agent"}) {
		t.Fatalf("UnrestoredSessions = %q, want the 2 saved sessions the scratch server lacks", got)
	}
}

func TestUnrestoredSessionsIsZeroOnceEverySavedSessionIsLive(t *testing.T) {
	t.Parallel()

	got := UnrestoredSessions(time.Unix(2_000, 0), time.Unix(1_000, 0), []string{"work", "agent"}, []string{"agent", "work", "extra"})
	if len(got) != 0 {
		t.Fatalf("UnrestoredSessions = %q, want none after a restore", got)
	}
}

func TestUnrestoredSessionsIgnoresSnapshotsSavedOnTheLiveServer(t *testing.T) {
	t.Parallel()

	started := time.Unix(2_000, 0)

	for name, finished := range map[string]time.Time{
		"after start":    time.Unix(2_001, 0),
		"unknown finish": {},
	} {
		if got := UnrestoredSessions(started, finished, []string{"work"}, nil); len(got) != 0 {
			t.Fatalf("%s: UnrestoredSessions = %q, want none", name, got)
		}
	}

	if got := UnrestoredSessions(time.Time{}, time.Unix(1_000, 0), []string{"work"}, nil); len(got) != 0 {
		t.Fatalf("unknown server start: UnrestoredSessions = %q, want none", got)
	}
}

func TestUnrestoredSessionsTreatsASaveInTheServerStartSecondAsOlder(t *testing.T) {
	t.Parallel()

	started := time.Unix(2_000, 0)
	finished := time.Unix(2_000, 400_000_000)

	if got := UnrestoredSessions(started, finished, []string{"work"}, []string{"scratch"}); !slices.Equal(got, []string{"work"}) {
		t.Fatalf("UnrestoredSessions = %q, want the saved session missing from a server restarted in the same second", got)
	}
}
