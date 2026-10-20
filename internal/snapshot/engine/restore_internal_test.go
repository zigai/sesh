//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errImportTest      = errors.New("import failed")
	errJournalSyncTest = errors.New("journal sync failed")
	errPhaseTest       = errors.New("phase failed")
)

func TestScopedImportFailurePreservesImportAndJournalCauses(t *testing.T) {
	importErr := errors.Join(errImportTest, context.Canceled)
	journalErr := errors.Join(store.ErrUncertain, errJournalSyncTest)

	err := scopedImportFailure(importErr, journalErr)

	for _, cause := range []error{restore.ErrExecutionUncertain, context.Canceled, store.ErrUncertain} {
		if !errors.Is(err, cause) {
			t.Fatalf("scoped import failure lost cause %v: %v", cause, err)
		}
	}
}

func TestDaemonIdentityFailureClassificationPreservesUncertainty(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantReason string
	}{
		{
			name:       "changed before effects",
			err:        tmuxserver.ErrDaemonIdentityChanged,
			wantCode:   "restore_conflict",
			wantReason: "daemon_identity_changed",
		},
		{
			name:       "unavailable before effects",
			err:        tmuxserver.ErrDaemonIdentityUnavailable,
			wantCode:   "restore_failed",
			wantReason: "daemon_identity_unavailable",
		},
		{
			name:       "changed after effects",
			err:        errors.Join(restore.ErrExecutionUncertain, tmuxserver.ErrDaemonIdentityChanged),
			wantCode:   "operation_uncertain",
			wantReason: "effect_outcome_uncertain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, reason := classifyRestoreFailure(tt.err)
			if code != tt.wantCode || reason != tt.wantReason {
				t.Fatalf("classification = %q/%q, want %q/%q", code, reason, tt.wantCode, tt.wantReason)
			}
		})
	}
}

func TestPostPhaseIdentityFailureAddsUncertaintyOnlyAfterDispatch(t *testing.T) {
	phaseErr := errPhaseTest
	identityErr := tmuxserver.ErrDaemonIdentityChanged

	beforeDispatch := joinPhaseIdentityFailure(phaseErr, identityErr, false)
	if !errors.Is(beforeDispatch, phaseErr) || !errors.Is(beforeDispatch, identityErr) {
		t.Fatalf("pre-dispatch failure lost a cause: %v", beforeDispatch)
	}

	if errors.Is(beforeDispatch, restore.ErrExecutionUncertain) {
		t.Fatalf("pre-dispatch failure was marked uncertain: %v", beforeDispatch)
	}

	afterDispatch := joinPhaseIdentityFailure(phaseErr, identityErr, true)
	for _, cause := range []error{phaseErr, identityErr, restore.ErrExecutionUncertain} {
		if !errors.Is(afterDispatch, cause) {
			t.Fatalf("post-dispatch failure lost cause %v: %v", cause, afterDispatch)
		}
	}
}

func TestExecutionMayHaveDispatchedUsesCurrentAttemptReceipts(t *testing.T) {
	tests := []struct {
		name       string
		report     restore.ExecutionReport
		candidates map[string]bool
		want       bool
	}{
		{name: "known pre-dispatch rejection", report: restore.ExecutionReport{}, candidates: map[string]bool{"create": true}, want: false},
		{name: "successful dispatch", report: restore.ExecutionReport{Dispatched: []string{"create"}}, candidates: map[string]bool{"create": true}, want: true},
		{name: "confirmed before install failed", report: restore.ExecutionReport{Receipts: map[string]string{"create": "$1"}}, candidates: map[string]bool{"create": true}, want: true},
		{name: "adopted prior receipt", report: restore.ExecutionReport{Receipts: map[string]string{"create": "$1"}}, candidates: map[string]bool{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := executionMayHaveDispatched(tt.report, tt.candidates); got != tt.want {
				t.Fatalf("executionMayHaveDispatched() = %t, want %t", got, tt.want)
			}
		})
	}
}

var errSecretRestoreCause = errors.New("secret /secret/path cause")

func restoreRecord(t *testing.T, data []byte) map[string]any {
	t.Helper()

	if lines := bytes.Count(data, []byte{'\n'}); lines != 1 {
		t.Fatalf("expected one record, got %d: %s", lines, data)
	}

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}

	for key := range record {
		switch key {
		case "level", "component", "operation", "status", "message", "reason", "phase", "sessions", "skipped", "verified",
			"mismatches", "mismatch_reasons", "failed_step", "step_reason", "duration_ms",
			"error", "error_domain", "error_code", "error_step", "generation", "restore_id",
			"resumed_panes", "restarted_panes", "shell_panes", "unrecovered_panes", "directory_fallback_panes":
		default:
			t.Fatalf("unexpected restore log field %s", key)
		}
	}

	if duration, ok := record["duration_ms"].(float64); !ok || duration < 0 {
		t.Fatalf("duration_ms = %v, want a non-negative number", record["duration_ms"])
	}

	return record
}

func TestRestoreFailureRecordSummarizesVerificationDifferences(t *testing.T) {
	var output bytes.Buffer

	ctx := context.WithValue(zerolog.New(&output).WithContext(t.Context()), restoreStartedKey{}, time.Now())

	report := restore.VerificationReport{Mismatches: []restore.VerificationMismatch{
		{Domain: restore.MismatchOption, Entity: "s1", Reason: "local_option_mismatch"},
		{Domain: restore.MismatchOption, Entity: "s2", Reason: "local_option_mismatch"},
		{Domain: restore.MismatchEnvironment, Entity: "s6", Reason: "scope_unreadable"},
	}}

	_ = restoreFailure(ctx, "verify", &verificationError{report: report})

	record := restoreRecord(t, output.Bytes())
	if record["level"] != "error" || record["phase"] != "verify" || record["mismatches"] != float64(3) ||
		record["mismatch_reasons"] != "environment/scope_unreadable,option/local_option_mismatch" {
		t.Fatalf("verification failure record = %v", record)
	}

	if bytes.Contains(output.Bytes(), []byte(`"s1"`)) || bytes.Contains(output.Bytes(), []byte(`"s6"`)) {
		t.Fatalf("record names restored entities: %s", output.Bytes())
	}
}

func TestRestoreFailureRecordNamesTheFailedStep(t *testing.T) {
	var output bytes.Buffer

	ctx := context.WithValue(zerolog.New(&output).WithContext(t.Context()), restoreStartedKey{}, time.Now())

	optionErr := oops.In("snapshot.restore").Code("operation_uncertain").
		With("operation", "import_options", "reason", "option_set_outcome_unknown").Wrap(errSecretRestoreCause)

	_ = restoreFailure(ctx, "import", scopedImportFailure(optionErr, nil))

	record := restoreRecord(t, output.Bytes())
	if record["failed_step"] != "import_options" || record["step_reason"] != "option_set_outcome_unknown" {
		t.Fatalf("failed step = %v / %v, want import_options / option_set_outcome_unknown", record["failed_step"], record["step_reason"])
	}

	if _, ok := record["mismatches"]; ok {
		t.Fatal("a failure without verification must not report mismatches")
	}
}

func TestRestoreSuccessRecordCountsProgramOutcomes(t *testing.T) {
	var output bytes.Buffer

	ctx := context.WithValue(zerolog.New(&output).WithContext(t.Context()), restoreStartedKey{}, time.Now())

	logRestoreSuccess(ctx, RestoreResult{
		Status: "best_effort", Reason: "fidelity_limited", SessionsRestored: []string{"$1", "$2"}, Verified: true,
		Panes: []restore.PaneResult{
			{PaneID: "p0", Outcome: restore.PaneOutcomeNativeResumeDispatch},
			{PaneID: "p1", Outcome: restore.PaneOutcomeNativeResumeDispatch, CWDFallback: true},
			{PaneID: "p2", Outcome: restore.PaneOutcomeRestartDispatch},
			{PaneID: "p3", Outcome: restore.PaneOutcomeDefaultShell},
			{PaneID: "p4", Outcome: restore.PaneOutcomeUnsupported},
		},
	})

	record := restoreRecord(t, output.Bytes())

	want := map[string]float64{"resumed_panes": 2, "restarted_panes": 1, "shell_panes": 1, "unrecovered_panes": 1, "directory_fallback_panes": 1, "sessions": 2}
	for field, value := range want {
		if record[field] != value {
			t.Fatalf("%s = %v, want %v", field, record[field], value)
		}
	}

	if record["level"] != "warn" || record["status"] != "best_effort" {
		t.Fatalf("restore success record = %v", record)
	}
}
