package snapshot

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

func summaryManifest() model.Manifest {
	return model.Manifest{
		Sessions: []model.Session{{ID: "s0", Name: model.Bytes("0")}, {ID: "s1", Name: model.Bytes("agent")}},
		Links:    []model.Link{{Session: "s0", Window: "w0", Index: 0}, {Session: "s1", Window: "w1", Index: 3}},
		Panes: []model.Pane{
			{ID: "p0", Window: "w0", Index: 0},
			{ID: "p1", Window: "w1", Index: 0, Launch: model.Launch{Executable: model.Bytes("/usr/bin/claude")}},
			{ID: "p2", Window: "w1", Index: 1, Launch: model.Launch{Executable: model.Bytes("/usr/bin/nvim")}},
			{ID: "p3", Window: "w1", Index: 2, Launch: model.Launch{Executable: model.Bytes("/usr/bin/node")}},
		},
	}
}

func summaryResult() engine.RestoreResult {
	return engine.RestoreResult{
		OperationID:      "op-1",
		Generation:       "d879e6dc156d244c6e807b18d070d558",
		Status:           "partial",
		Reason:           "sessions_skipped",
		SessionsRestored: []string{"$1"},
		Verified:         true,
		Panes: []restore.PaneResult{
			{PaneID: "p1", RuntimeID: "%1", Outcome: restore.PaneOutcomeNativeResumeDispatch},
			{PaneID: "p2", RuntimeID: "%2", Outcome: restore.PaneOutcomeRestartDispatch},
			{PaneID: "p3", RuntimeID: "%3", Outcome: restore.PaneOutcomeUnsupported, Reason: "executable_deleted"},
		},
		SessionsSkipped: []engine.SkippedSession{{ID: "s0", Name: "0", Reason: restore.CollisionNameInUse}},
	}
}

func renderSummary(t *testing.T, res engine.RestoreResult, labels restoreLabels, verbose bool) string {
	t.Helper()

	cmd := newSnapshotRestoreCommand()
	if verbose {
		if err := cmd.Flags().Set("verbose", "true"); err != nil {
			t.Fatal(err)
		}
	}

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := renderRestoreTable(cmd, res, labels, nil); err != nil {
		t.Fatalf("renderRestoreTable: %v", err)
	}

	return stdout.String()
}

func TestRestoreSummaryNamesSessionsProgramsAndNextSteps(t *testing.T) {
	t.Parallel()

	got := renderSummary(t, summaryResult(), newRestoreLabels(summaryManifest()), false)

	want := "Restored 1 session from generation d879e6dc: agent\n" +
		"Programs: 1 resumed, 1 restarted, 1 need attention\n" +
		"  agent:3.2  node: its program file was replaced while it ran (for example by an upgrade); start it again yourself\n" +
		"Resumed and restarted programs were launched, but sesh cannot confirm they recovered; check those panes.\n" +
		"Skipped:\n" +
		"  0 (s0): a live session already uses this name\n" +
		"  To restore one, rename the live session (tmux rename-session -t \"0\" <new-name>), then run:\n" +
		"    sesh snapshot restore --generation d879e6dc156d244c6e807b18d070d558 --session s0\n"
	if got != want {
		t.Fatalf("summary:\n%s\nwant:\n%s", got, want)
	}

	for _, internal := range []string{"runtime=", "parity", "op-1", "%3"} {
		if strings.Contains(got, internal) {
			t.Fatalf("the default summary must not show %q:\n%s", internal, got)
		}
	}
}

func TestRestoreSummaryVerboseAddsPerPaneDetails(t *testing.T) {
	t.Parallel()

	got := renderSummary(t, summaryResult(), newRestoreLabels(summaryManifest()), true)

	for _, want := range []string{"Restored 1 session", "Operation:          op-1", `p3 runtime="%3" outcome=unsupported reason="executable_deleted"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose output is missing %q:\n%s", want, got)
		}
	}
}

func TestRestoreSummaryWithoutLabelsFallsBackToIDs(t *testing.T) {
	t.Parallel()

	got := renderSummary(t, summaryResult(), restoreLabels{}, false)

	for _, want := range []string{"Restored 1 session from generation d879e6dc.\n", "  p3  its program file was replaced"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary without labels is missing %q:\n%s", want, got)
		}
	}
}

func TestRestoreSummaryOffersSafeResumeWhenEffectsAreUncertain(t *testing.T) {
	t.Parallel()

	res := summaryResult()
	res.Uncertain = true

	got := renderSummary(t, res, newRestoreLabels(summaryManifest()), false)
	if !strings.Contains(got, "sesh snapshot restore --generation d879e6dc156d244c6e807b18d070d558 --operation op-1") {
		t.Fatalf("an uncertain restore must say how to continue it safely:\n%s", got)
	}
}
