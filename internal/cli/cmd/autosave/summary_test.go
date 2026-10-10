package autosave

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/automation"
)

func summaryEnv() Env {
	return Env{
		workspace:     app.Workspace{},
		interval:      3 * time.Minute,
		historyKnown:  true,
		historyErr:    nil,
		decision:      automation.DueDecision{Due: false, Reason: automation.ReasonNotDue, Elapsed: 80 * time.Second},
		lastSavedUnix: 0,
	}
}

func TestAutosaveSummarySaysWhatHappenedInOneLine(t *testing.T) {
	t.Parallel()

	saved := newOutcome(statusSaved, automation.ReasonDurableGeneration)
	saved.generation = "d879e6dc156d244c6e807b18d070d558"

	notDue := newOutcome("not_due", automation.ReasonNotDue)

	pending := newOutcome(statusSkipped, reasonRestorePending)
	pending.restorable = 2

	refused := newOutcome(statusSkipped, automation.ReasonPendingOperation)
	refused.abandoned = 1

	restored := newOutcome("complete", "verified")
	restored.restore, restored.restored, restored.generation = true, 2, "d879e6dc156d244c6e807b18d070d558"

	partial := newOutcome("partial", "sessions_skipped")
	partial.restore, partial.restored, partial.skipped, partial.generation = true, 1, 1, "d879e6dc156d244c6e807b18d070d558"

	tests := []struct {
		name    string
		outcome outcome
		want    string
	}{
		{"saved", saved, "Saved generation d879e6dc.\n"},
		{"not due", notDue, "Not due: last save 1m20s ago, saves run every 3m.\n"},
		{"held back until restored", pending, "Skipped: tmux started after the newest snapshot and its saved sessions are not restored yet.\n" +
			"2 saved sessions are not running. Restore with: sesh snapshot restore\n" +
			"Or keep the current state, which resumes autosave: sesh snapshot save\n"},
		{"refused restore", refused, "Not restoring: an earlier restore is still running or was interrupted.\n" +
			"Abandoned 1 interrupted restore whose tmux server no longer exists.\n"},
		{"restored", restored, "Restored 2 sessions from generation d879e6dc.\n"},
		{"partly restored", partial, "Restored 1 session from generation d879e6dc.\n" +
			"Skipped 1 session whose names are already in use; sesh snapshot restore shows them and how to restore them.\n"},
		{"unknown skip", newOutcome(statusSkipped, "clients_attached"), "Skipped (clients_attached).\n"},
	}

	for _, tc := range tests {
		if got := summary(summaryEnv(), tc.outcome); got != tc.want {
			t.Errorf("%s:\n%q\nwant:\n%q", tc.name, got, tc.want)
		}
	}
}

func TestAutosaveStatusSummaryShowsScheduleSnapshotsAndShutdownSaves(t *testing.T) {
	t.Parallel()

	outcome := newOutcome("not_due", automation.ReasonNotDue)
	outcome.history = historyRecorded
	outcome.repository = repositoryReady
	outcome.daemonState = daemonReady
	outcome.finalSave = automation.FinalSaveIncomplete
	outcome.finalSaveAge = 50 * time.Hour

	want := "Autosave:        on, every 3m; last save 1m20s ago\n" +
		"Snapshots:       ready to restore\n" +
		"Before shutdown: saves run before sleep and shutdown\n" +
		"Last final save: cut short 2d2h ago; restore uses the newest finished save\n"
	if got := statusSummary(summaryEnv(), outcome); got != want {
		t.Fatalf("status:\n%s\nwant:\n%s", got, want)
	}

	outcome.daemonState = DaemonUnselected
	outcome.finalSave = ""
	outcome.repository = repositoryRecovery

	want = "Autosave:        on, every 3m; last save 1m20s ago\n" +
		"Snapshots:       a restore is unfinished; run sesh doctor\n"
	if got := statusSummary(summaryEnv(), outcome); got != want {
		t.Fatalf("status without a coordinator:\n%s\nwant:\n%s", got, want)
	}
}

func TestShortDurationReadsLikeSpeech(t *testing.T) {
	t.Parallel()

	tests := []struct {
		duration time.Duration
		want     string
	}{
		{3 * time.Minute, "3m"},
		{80 * time.Second, "1m20s"},
		{2*time.Hour + 5*time.Minute, "2h5m"},
		{2 * time.Hour, "2h"},
		{1500 * time.Millisecond, "2s"},
		{24 * time.Hour, "1d"},
		{3*24*time.Hour + 5*time.Hour + 20*time.Minute, "3d5h"},
	}

	for _, tc := range tests {
		if got := shortDuration(tc.duration); got != tc.want {
			t.Errorf("shortDuration(%s) = %q, want %q", tc.duration, got, tc.want)
		}
	}
}
