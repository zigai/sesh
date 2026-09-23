//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"testing"

	integration "github.com/zigai/sesh/internal/integration/tmux"
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
			err:        integration.ErrDaemonIdentityChanged,
			wantCode:   "restore_conflict",
			wantReason: "daemon_identity_changed",
		},
		{
			name:       "unavailable before effects",
			err:        integration.ErrDaemonIdentityUnavailable,
			wantCode:   "restore_failed",
			wantReason: "daemon_identity_unavailable",
		},
		{
			name:       "changed after effects",
			err:        errors.Join(restore.ErrExecutionUncertain, integration.ErrDaemonIdentityChanged),
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
	identityErr := integration.ErrDaemonIdentityChanged

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
