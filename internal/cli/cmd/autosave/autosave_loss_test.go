package autosave

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type lossFixture struct {
	repo        *store.Repository
	historyPath string
	buffer      *bytes.Buffer
	env         Env
}

func newLossFixture(t *testing.T, committed bool) *lossFixture {
	t.Helper()

	repo, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "snapshots"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	if committed {
		commitMaintenanceObservation(t, repo)
	}

	historyPath := filepath.Join(t.TempDir(), "state", "history.json")

	return &lossFixture{
		repo:        repo,
		historyPath: historyPath,
		buffer:      &bytes.Buffer{},
		env:         Env{workspace: app.Workspace{History: historyPath}, lastSavedUnix: 1_000},
	}
}

func (f *lossFixture) report(t *testing.T) int {
	t.Helper()

	ctx := zerolog.New(f.buffer).WithContext(t.Context())

	return reportRestorableSessions(ctx, f.env, f.repo)
}

func (f *lossFixture) records(t *testing.T) []map[string]any {
	t.Helper()

	var records []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(f.buffer.String()), "\n") {
		if line == "" {
			continue
		}

		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding log record failed: %v", err)
		}

		records = append(records, record)
	}

	return records
}

func TestMissingServerWithSavedSessionsIsReportedOncePerAutosaveEpisode(t *testing.T) {
	fixture := newLossFixture(t, true)

	if count := fixture.report(t); count != 1 {
		t.Fatalf("first interval after the loss must report the saved session, got %d", count)
	}

	records := fixture.records(t)
	if len(records) != 1 {
		t.Fatalf("expected exactly one log record, got %d: %s", len(records), fixture.buffer.String())
	}

	record := records[0]
	if record["level"] != "warn" || record["component"] != "snapshot" || record["operation"] != "autosave" ||
		record["status"] != "skipped" || record["reason"] != "restorable_sessions" || record["sessions"] != float64(1) {
		t.Fatalf("unexpected record schema: %+v", record)
	}

	if count := fixture.report(t); count != 0 {
		t.Fatalf("the same loss must not be reported again, got %d", count)
	}

	if len(fixture.records(t)) != 1 {
		t.Fatalf("a repeated interval must stay silent: %s", fixture.buffer.String())
	}

	fixture.env.lastSavedUnix = 2_000

	if count := fixture.report(t); count != 1 {
		t.Fatalf("a loss after a newer recorded autosave is a new episode, got %d", count)
	}
}

func TestRestorableSessionReportNamesNothingPrivate(t *testing.T) {
	fixture := newLossFixture(t, true)

	fixture.report(t)

	for _, private := range []string{"work", fixture.historyPath, filepath.Dir(fixture.historyPath)} {
		if strings.Contains(fixture.buffer.String(), private) {
			t.Fatalf("the log must not contain session names or paths (%q): %s", private, fixture.buffer.String())
		}
	}
}

func TestMissingServerStaysSilentWithoutAnythingToRestore(t *testing.T) {
	t.Run("no recorded autosave", func(t *testing.T) {
		fixture := newLossFixture(t, true)
		fixture.env.lastSavedUnix = 0

		if count := fixture.report(t); count != 0 || fixture.buffer.Len() != 0 {
			t.Fatalf("expected silence, got %d and %q", count, fixture.buffer.String())
		}
	})

	t.Run("no history path", func(t *testing.T) {
		fixture := newLossFixture(t, true)
		fixture.env.workspace.History = ""

		if count := fixture.report(t); count != 0 || fixture.buffer.Len() != 0 {
			t.Fatalf("expected silence, got %d and %q", count, fixture.buffer.String())
		}
	})

	t.Run("nothing saved", func(t *testing.T) {
		fixture := newLossFixture(t, false)

		if count := fixture.report(t); count != 0 || fixture.buffer.Len() != 0 {
			t.Fatalf("expected silence, got %d and %q", count, fixture.buffer.String())
		}
	})

	t.Run("unreadable marker", func(t *testing.T) {
		fixture := newLossFixture(t, true)

		markerPath := automation.ServerLossMarkerPath(fixture.historyPath)
		if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(markerPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		if count := fixture.report(t); count != 0 || fixture.buffer.Len() != 0 {
			t.Fatalf("an unreadable marker must not turn into a report on every interval, got %d and %q", count, fixture.buffer.String())
		}
	})
}

func TestNothingSavedDoesNotConsumeTheEpisode(t *testing.T) {
	fixture := newLossFixture(t, false)

	if count := fixture.report(t); count != 0 {
		t.Fatalf("expected silence without a snapshot, got %d", count)
	}

	commitMaintenanceObservation(t, fixture.repo)

	if count := fixture.report(t); count != 1 {
		t.Fatalf("a snapshot that appears later must still be reported, got %d", count)
	}
}

func TestRestorableLineIsOnlyShownWhenSessionsAreWaiting(t *testing.T) {
	t.Parallel()

	idle := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonTmuxNotRunning)
	if restorableLine(idle) != "" {
		t.Fatal("no waiting sessions must add no output")
	}

	idle.restorable = 3
	if line := restorableLine(idle); !strings.Contains(line, "3 saved sessions can be restored") || !strings.Contains(line, "Start tmux") ||
		!strings.Contains(line, "sesh snapshot restore") {
		t.Fatalf("unexpected line: %q", line)
	}

	pending := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonRestorePending)
	pending.restorable = 2

	line := restorableLine(pending)
	if !strings.Contains(line, "2 saved sessions are not running") || !strings.Contains(line, "sesh snapshot restore") ||
		!strings.Contains(line, "sesh snapshot save") {
		t.Fatalf("unexpected restore-pending line: %q", line)
	}
}
