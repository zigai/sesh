package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

type restoreTestEnvelope struct {
	OK     bool                      `json:"ok"`
	Result snapshotRestoreResultJSON `json:"result"`
	Error  *clibase.JSONErrorPayload `json:"error"`
}

func renderRestoreJSONForTest(t *testing.T, result engine.RestoreResult, cause error) (restoreTestEnvelope, error) {
	t.Helper()

	cmd := newSnapshotRestoreCommand()

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Flags().Set(jsonFlagName, "true"); err != nil {
		t.Fatal(err)
	}

	resultErr := renderRestoreOutcome(cmd, result, restoreLabels{}, nil, cause)
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

	result := engine.RestoreResult{
		OperationID:         "0123456789abcdef0123456789abcdef",
		Generation:          "abcdef0123456789abcdef0123456789",
		Status:              "uncertain",
		Reason:              "effect_outcome_uncertain",
		SessionsRestored:    nil,
		Verified:            false,
		Uncertain:           true,
		MissingRequirements: nil,
		Panes:               nil,
	}

	cases := []struct {
		name  string
		cause error
		code  string
	}{
		{"effect", errors.Join(restore.ErrExecutionUncertain, context.Canceled), "operationUncertain"},
		{"close", errSensitiveRaw, "operationFailed"},
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

func TestRestoreJSONEnvelopeOmitsCompensation(t *testing.T) {
	t.Parallel()

	result := engine.RestoreResult{OperationID: "0123456789abcdef0123456789abcdef", Status: "best_effort"}

	direct, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var directFields map[string]json.RawMessage
	if err := json.Unmarshal(direct, &directFields); err != nil {
		t.Fatal(err)
	}

	if _, ok := directFields["compensation"]; ok {
		t.Fatalf("restore result contains compensation: %s", direct)
	}

	cmd := newSnapshotRestoreCommand()

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := cmd.Flags().Set(jsonFlagName, "true"); err != nil {
		t.Fatal(err)
	}

	if err := renderRestoreOutcome(cmd, result, restoreLabels{}, nil, nil); err != nil {
		t.Fatal(err)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["result"], &fields); err != nil {
		t.Fatal(err)
	}

	if _, ok := fields["compensation"]; ok {
		t.Fatalf("restore JSON result contains compensation: %s", stdout.String())
	}
}

func TestPartialRestoreReportsDegradedResult(t *testing.T) {
	t.Parallel()

	result := engine.RestoreResult{
		OperationID:         "0123456789abcdef0123456789abcdef",
		Generation:          "abcdef0123456789abcdef0123456789",
		Status:              "partial",
		Reason:              "parity_incomplete",
		SessionsRestored:    []string{"restored"},
		Verified:            true,
		Uncertain:           false,
		MissingRequirements: []restore.MissingRequirement{{Domain: "terminal", Status: "unsupported", Reason: "terminal_checkpoint_unavailable"}},
		Panes:               nil,
	}

	envelope, err := renderRestoreJSONForTest(t, result, nil)
	if !errors.Is(err, errPartialRestore) || clibase.ExitCode(err) != clibase.ExitCodeGeneral {
		t.Fatalf("partial restore was reported as success: %v", err)
	}

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "partialRestore" {
		t.Fatalf("expected one degraded envelope: %+v", envelope)
	}

	if envelope.Result.OperationID != result.OperationID || len(envelope.Result.MissingRequirements) != 1 || !envelope.Result.Verified {
		t.Fatalf("lost partial outcome evidence: %+v", envelope.Result)
	}
}

func TestRestoreOperationFlagSelectsOperationAndKeepsSessionArguments(t *testing.T) {
	t.Parallel()

	for _, cmd := range []*cobra.Command{newSnapshotRestoreCommand(), NewActionRestoreCommand()} {
		for name, value := range map[string]string{
			"repository": t.TempDir(),
			"socket":     "/tmp/selected-test-server",
			"lease-root": t.TempDir(),
			"operation":  "0123456789abcdef0123456789abcdef",
		} {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatal(err)
			}
		}

		_, _, cfg, err := parseRestoreInput(cmd, []string{"$1"})
		if err != nil {
			t.Fatal(err)
		}

		if cfg.OperationID != "0123456789abcdef0123456789abcdef" || len(cfg.Sessions) != 1 || cfg.Sessions[0] != "$1" {
			t.Fatalf("%s config = operation %q sessions %v, want the operation and session $1", cmd.Name(), cfg.OperationID, cfg.Sessions)
		}
	}
}

func TestRestoreJSONProjectionExposesPanes(t *testing.T) {
	t.Parallel()

	paneResult := restore.PaneResult{
		PaneID:      "source-pane-1",
		RuntimeID:   "%7",
		Outcome:     restore.PaneOutcomeAllowedCWDFallback,
		Reason:      restore.PaneReasonWorkingDirFallback,
		AppVerified: false,
		CWDFallback: true,
	}

	result := engine.RestoreResult{
		OperationID:         "0123456789abcdef0123456789abcdef",
		Generation:          "abcdef0123456789abcdef0123456789",
		Status:              "complete",
		Reason:              "restored",
		SessionsRestored:    []string{"session-1"},
		Panes:               []restore.PaneResult{paneResult},
		Verified:            true,
		Uncertain:           false,
		MissingRequirements: nil,
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

	if err := renderRestoreTable(cmd, result, restoreLabels{}, nil); err != nil {
		t.Fatalf("renderRestoreTable failed: %v", err)
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

	result := engine.RestoreResult{
		Status: "failed",
		Reason: "collision",
		SessionsSkipped: []engine.SkippedSession{
			{ID: "s38", Name: "0", Reason: restore.CollisionNameInUse},
		},
	}

	cmd := newSnapshotRestoreCommand()

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	err := renderRestoreOutcome(cmd, result, restoreLabels{}, nil, clibase.NewRestoreBoundaryError(restore.ErrSessionCollision))
	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("expected the collision error to be preserved, got %v", err)
	}

	for _, want := range []string{"Skipped:", "0 (s38): a live session already uses this name", `tmux rename-session -t "0"`, "sesh snapshot restore --session s38"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected %q in output:\n%s", want, stdout.String())
		}
	}
}

func TestRestoreJSONReportsSkippedSessionsAndAdvisories(t *testing.T) {
	t.Parallel()

	result := engine.RestoreResult{
		OperationID:      "op-1",
		Generation:       "gen-1",
		Status:           "partial",
		Reason:           "sessions_skipped",
		SessionsRestored: []string{"s1"},
		Verified:         true,
		SessionsSkipped:  []engine.SkippedSession{{ID: "s38", Name: "0", Reason: restore.CollisionNameInUse}},
		Advisories:       []restore.VerificationMismatch{{Domain: restore.MismatchPane, Entity: "p1", Reason: "pane_title_changed"}},
	}

	envelope, err := renderRestoreJSONForTest(t, result, nil)
	if !errors.Is(err, errPartialRestore) {
		t.Fatalf("a restore that skipped sessions must exit as partial, got %v", err)
	}

	if len(envelope.Result.SessionsSkipped) != 1 || envelope.Result.SessionsSkipped[0].Name != "0" {
		t.Fatalf("skipped sessions missing from JSON: %+v", envelope.Result)
	}

	if len(envelope.Result.Advisories) != 1 || envelope.Result.Advisories[0].Reason != "pane_title_changed" {
		t.Fatalf("advisories missing from JSON: %+v", envelope.Result)
	}
}

func TestRestoreRejectsUnknownCollisionPolicy(t *testing.T) {
	t.Parallel()

	cmd := newSnapshotRestoreCommand()
	if err := cmd.Flags().Set("on-collision", "replace"); err != nil {
		t.Fatal(err)
	}

	_, err := parseCollisionPolicy(cmd)
	if err == nil || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("expected a usage error for an unknown policy, got %v", err)
	}
}
