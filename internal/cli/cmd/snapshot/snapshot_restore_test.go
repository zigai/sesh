package cmdsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

	resultErr := renderRestoreOutcome(cmd, result, cause)
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
		Compensation:        nil,
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
		Compensation:        nil,
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

func TestRestoreOperationFlagReachesEngineConfiguration(t *testing.T) {
	t.Parallel()

	for _, action := range []bool{false, true} {
		cmd := newSnapshotRestoreCommand()
		if action {
			cmd = NewActionRestoreCommand()
		}

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

		_, _, cfg, err := parseRestoreInput(cmd, nil)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.OperationID != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("operation flag lost for %s", cmd.Name())
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
		Compensation:        nil,
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

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := renderRestoreTable(cmd, result); err != nil {
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
