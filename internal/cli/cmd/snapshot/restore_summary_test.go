package snapshot

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

func summaryResult() action.RestoredSnapshot {
	return action.RestoredSnapshot{
		OperationID: "op-1",
		Generation:  "d879e6dc156d244c6e807b18d070d558",
		Status:      "partial",
		Reason:      "sessions_skipped",
		Sessions:    []action.SessionRef{{Kind: session.KindLive, ID: "$1", Name: "agent"}},
		Verified:    true,
		Panes: []action.RestoredPane{
			{PaneID: "p1", RuntimeID: "%1", Outcome: restore.PaneOutcomeNativeResumeDispatch, Location: "agent:3.0", Program: "claude"},
			{PaneID: "p2", RuntimeID: "%2", Outcome: restore.PaneOutcomeRestartDispatch, Location: "agent:3.1", Program: "nvim"},
			{PaneID: "p3", RuntimeID: "%3", Outcome: restore.PaneOutcomeUnsupported, Reason: "executable_deleted", Location: "agent:3.2", Program: "node"},
		},
		SessionsSkipped: []action.SkippedSession{{ID: "s0", Name: "0", Reason: restore.CollisionNameInUse}},
	}
}

func renderSummary(t *testing.T, res action.RestoredSnapshot, verbose bool) string {
	t.Helper()

	cmd := newSnapshotRestoreCommand()
	if verbose {
		if err := cmd.Flags().Set("verbose", "true"); err != nil {
			t.Fatal(err)
		}
	}

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := renderSnapshotRestore(cmd, res, nil); err != nil {
		t.Fatalf("renderSnapshotRestore: %v", err)
	}

	return stdout.String()
}

func TestRestoreSummaryNamesSessionsProgramsAndNextSteps(t *testing.T) {
	t.Parallel()

	got := renderSummary(t, summaryResult(), false)

	want := "Restored 1 session from generation d879e6dc: agent\n" +
		"Programs: 1 resumed, 1 restarted, 1 need attention\n" +
		"  agent:3.2  node: its program file was replaced while it ran (for example by an upgrade); start it again yourself\n" +
		"Resumed and restarted programs were launched, but sesh cannot confirm they recovered; check those panes.\n" +
		"Skipped:\n" +
		"  0 (s0): a live session already uses this name\n" +
		"  To restore one, rename the live session (tmux rename-session -t \"0\" <new-name>), then run:\n" +
		"    sesh snapshot restore --generation d879e6dc156d244c6e807b18d070d558 s0\n"
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

	got := renderSummary(t, summaryResult(), true)

	for _, want := range []string{"Restored 1 session", "Operation:          op-1", `p3 runtime="%3" outcome=unsupported reason="executable_deleted"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose output is missing %q:\n%s", want, got)
		}
	}
}

func TestRestoreSummaryWithoutSessionNamesCountsSessions(t *testing.T) {
	t.Parallel()

	res := summaryResult()
	res.Sessions[0].Name = ""
	res.Panes[2].Location, res.Panes[2].Program = "p3", ""

	got := renderSummary(t, res, false)

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

	got := renderSummary(t, res, false)
	if !strings.Contains(got, "sesh snapshot restore --generation d879e6dc156d244c6e807b18d070d558 --operation op-1") {
		t.Fatalf("an uncertain restore must say how to continue it safely:\n%s", got)
	}
}

func TestRestoreSummaryListsLayoutDifferencesWhenVerbose(t *testing.T) {
	t.Parallel()

	res := summaryResult()
	res.Verified = false
	res.Mismatches = []action.Mismatch{{Domain: restore.MismatchPane, Entity: "p2", Reason: "pane_geometry_mismatch"}}

	summary := renderSummary(t, res, false)
	if !strings.Contains(summary, "does not fully match the snapshot") {
		t.Fatalf("a mismatched restore must say so:\n%s", summary)
	}

	verbose := renderSummary(t, res, true)
	if !strings.Contains(verbose, "Differences:\n  pane p2 reason=pane_geometry_mismatch\n") {
		t.Fatalf("verbose output must list each difference:\n%s", verbose)
	}
}

func TestRestoreSummaryDoesNotReportDifferencesBeforeVerification(t *testing.T) {
	t.Parallel()

	res := summaryResult()
	res.Verified = false
	res.Uncertain = true

	got := renderSummary(t, res, true)
	if strings.Contains(got, "does not fully match") || strings.Contains(got, "Differences:") {
		t.Fatalf("a restore that never verified must not claim layout differences:\n%s", got)
	}
}
