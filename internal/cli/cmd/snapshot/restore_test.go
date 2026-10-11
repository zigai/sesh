package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

type restoreTestEnvelope struct {
	OK     bool                      `json:"ok"`
	Result action.RestoredSnapshot   `json:"result"`
	Error  *clibase.JSONErrorPayload `json:"error"`
}

func renderRestoreJSONForTest(t *testing.T, result action.RestoredSnapshot, cause error) (restoreTestEnvelope, error) {
	t.Helper()

	cmd := newSnapshotRestoreCommand()

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Flags().Set(jsonFlagName, "true"); err != nil {
		t.Fatal(err)
	}

	resultErr := renderSnapshotRestore(cmd, result, cause)
	if err := clibase.WriteCommandError(cmd, resultErr); err != nil {
		t.Fatal(err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("degraded outcome duplicated error: %s", stderr.String())
	}

	var envelope restoreTestEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(stdout.String(), "/private/") {
		t.Fatal("raw cause leaked into restore output")
	}

	return envelope, resultErr
}

func TestRestoreFailureRetainsOperationReceipt(t *testing.T) {
	t.Parallel()

	result := action.RestoredSnapshot{
		OperationID: "0123456789abcdef0123456789abcdef",
		Generation:  "abcdef0123456789abcdef0123456789",
		Status:      "uncertain",
		Reason:      "effect_outcome_uncertain",
		Verified:    false,
		Uncertain:   true,
	}

	cases := []struct {
		name  string
		cause error
		code  string
	}{
		{"effect", action.Wrap(action.CodeUncertain, errors.Join(restore.ErrExecutionUncertain, context.Canceled)), "uncertain"},
		{"close", action.Wrap(action.CodeFailed, errSensitiveRaw), "operationFailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			envelope, err := renderRestoreJSONForTest(t, result, tc.cause)
			if !errors.Is(err, tc.cause) {
				t.Fatalf("restore cause lost: %v", err)
			}

			if envelope.OK || envelope.Error == nil || envelope.Error.Code != tc.code {
				t.Fatalf("failure envelope: %+v", envelope)
			}

			if envelope.Result.OperationID != result.OperationID || !envelope.Result.Uncertain || envelope.Result.Verified {
				t.Fatalf("recovery receipt was lost: %+v", envelope.Result)
			}
		})
	}
}

func TestPartialRestoreReportsDegradedResult(t *testing.T) {
	t.Parallel()

	result := action.RestoredSnapshot{
		OperationID:         "0123456789abcdef0123456789abcdef",
		Generation:          "abcdef0123456789abcdef0123456789",
		Status:              "partial",
		Reason:              "parity_incomplete",
		Sessions:            []action.SessionRef{{Kind: session.KindLive, ID: "$1", Name: "restored"}},
		Verified:            true,
		MissingRequirements: []action.Requirement{{Domain: "terminal", Status: "unsupported", Reason: "terminal_checkpoint_unavailable"}},
	}

	envelope, err := renderRestoreJSONForTest(t, result, action.Wrap(action.CodePartial, app.ErrPartialRestore))
	if !errors.Is(err, app.ErrPartialRestore) || clibase.ExitCode(err) != clibase.ExitCodeGeneral {
		t.Fatalf("partial restore was reported as success: %v", err)
	}

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "partial" {
		t.Fatalf("expected one degraded envelope: %+v", envelope)
	}

	if envelope.Result.OperationID != result.OperationID || len(envelope.Result.MissingRequirements) != 1 || !envelope.Result.Verified {
		t.Fatalf("lost partial outcome evidence: %+v", envelope.Result)
	}
}

func TestRestoreJSONProjectionExposesPanes(t *testing.T) {
	t.Parallel()

	paneResult := action.RestoredPane{
		PaneID:      "source-pane-1",
		RuntimeID:   "%7",
		Outcome:     restore.PaneOutcomeAllowedCWDFallback,
		Reason:      restore.PaneReasonWorkingDirFallback,
		AppVerified: false,
		CWDFallback: true,
		Location:    "work:0.0",
		Program:     "",
	}

	result := action.RestoredSnapshot{
		OperationID: "0123456789abcdef0123456789abcdef",
		Generation:  "abcdef0123456789abcdef0123456789",
		Status:      "complete",
		Reason:      "restored",
		Sessions:    []action.SessionRef{{Kind: session.KindLive, ID: "$1", Name: "work"}},
		Panes:       []action.RestoredPane{paneResult},
		Verified:    true,
	}

	envelope, err := renderRestoreJSONForTest(t, result, nil)
	if err != nil {
		t.Fatalf("renderRestoreJSONForTest failed: %v", err)
	}

	if !envelope.OK {
		t.Fatalf("expected OK envelope: %+v", envelope)
	}

	if len(envelope.Result.Panes) != 1 {
		t.Fatalf("expected 1 pane in result, got %d", len(envelope.Result.Panes))
	}

	if envelope.Result.Panes[0] != paneResult {
		t.Fatalf("pane result mismatch: got %+v, want %+v", envelope.Result.Panes[0], paneResult)
	}

	cmd := newSnapshotRestoreCommand()
	if err := cmd.Flags().Set("verbose", "true"); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := renderSnapshotRestore(cmd, result, nil); err != nil {
		t.Fatalf("renderSnapshotRestore failed: %v", err)
	}

	wantPaneLine := "  source-pane-1 runtime=\"%7\" outcome=allowed_cwd_fallback reason=\"saved_working_directory_missing\" app-verified=false cwd-fallback=true\n"
	if !strings.Contains(stdout.String(), wantPaneLine) {
		t.Fatalf("text pane evidence missing:\n%s", stdout.String())
	}

	resultNoPanes := result
	resultNoPanes.Panes = nil

	envelopeNoPanes, err := renderRestoreJSONForTest(t, resultNoPanes, nil)
	if err != nil {
		t.Fatalf("renderRestoreJSONForTest (no panes) failed: %v", err)
	}

	if len(envelopeNoPanes.Result.Panes) != 0 {
		t.Fatalf("expected empty panes, got %+v", envelopeNoPanes.Result.Panes)
	}
}

func TestRestoreRefusedForCollisionNamesSessionsWithoutAnOperation(t *testing.T) {
	t.Parallel()

	result := action.RestoredSnapshot{
		Status: "failed",
		Reason: "collision",
		SessionsSkipped: []action.SkippedSession{
			{ID: "s38", Name: "0", Reason: restore.CollisionNameInUse},
		},
	}

	cmd := newSnapshotRestoreCommand()

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	err := renderSnapshotRestore(cmd, result, action.Wrap(action.CodeAlreadyExists, restore.ErrSessionCollision))
	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("expected the collision error to be preserved, got %v", err)
	}

	for _, want := range []string{"Skipped:", "0 (s38): a live session already uses this name", `tmux rename-session -t "0"`, "sesh snapshot restore s38"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected %q in output:\n%s", want, stdout.String())
		}
	}
}

func TestRestoreJSONReportsSkippedSessionsAndAdvisories(t *testing.T) {
	t.Parallel()

	result := action.RestoredSnapshot{
		OperationID:     "op-1",
		Generation:      "gen-1",
		Status:          "partial",
		Reason:          "sessions_skipped",
		Sessions:        []action.SessionRef{{Kind: session.KindLive, ID: "$1", Name: "kept"}},
		Verified:        true,
		SessionsSkipped: []action.SkippedSession{{ID: "s38", Name: "0", Reason: restore.CollisionNameInUse}},
		Advisories:      []action.Mismatch{{Domain: restore.MismatchPane, Entity: "p1", Reason: "pane_title_changed"}},
	}

	envelope, err := renderRestoreJSONForTest(t, result, action.Wrap(action.CodePartial, app.ErrPartialRestore))
	if !errors.Is(err, app.ErrPartialRestore) {
		t.Fatalf("a restore that skipped sessions must exit as partial, got %v", err)
	}

	if len(envelope.Result.SessionsSkipped) != 1 || envelope.Result.SessionsSkipped[0].Name != "0" {
		t.Fatalf("skipped sessions missing from JSON: %+v", envelope.Result)
	}

	if len(envelope.Result.Advisories) != 1 || envelope.Result.Advisories[0].Reason != "pane_title_changed" {
		t.Fatalf("advisories missing from JSON: %+v", envelope.Result)
	}
}
