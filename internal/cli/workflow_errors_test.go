package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/agents"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestWorkflowErrorsPreservePublicClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cause error
		code  string
	}{
		{errDoctorChecksFailed, "doctorChecksFailed"},
		{session.ErrNotInsideTmux, "notInsideTmux"},
		{session.ErrInvokingClientUnavailable, "clientUnavailable"},
		{session.ErrInvokingClientAmbiguous, "clientAmbiguous"},
		{session.ErrSwitchUnverified, "switchUnverified"},
		{session.ErrTerminalRequired, "interactiveTerminalRequired"},
		{session.ErrSessionAlreadyExists, "sessionAlreadyExists"},
		{session.ErrSessionAttached, "sessionAttached"},
		{session.ErrSessionReplaced, "sessionReplaced"},
		{session.ErrSavedGenerationsProtected, "savedGenerationsProtected"},
		{session.ErrSavedSessionScopeConflict, "savedSessionScopeConflict"},
		{agents.ErrRegistrySchema, "agentRegistrySchema"},
		{agents.ErrRegistryCorrupt, "agentRegistryCorrupt"},
		{agents.ErrRegistryUnavailable, "agentRegistryUnavailable"},
		{agents.ErrRegistryTooLarge, "agentRegistryTooLarge"},
		{agents.ErrStyleOwnershipCorrupt, "agentStyleOwnershipCorrupt"},
		{agents.ErrCurrentContextUnknown, "agentContextUnknown"},
		{agents.ErrCurrentPaneNotLive, "agentPaneNotLive"},
		{agents.ErrNoTargetPane, "agentPaneNotFound"},
		{agents.ErrEmptyNavigationHistory, "agentHistoryEmpty"},
		{restore.ErrPriorConflict, "priorEffectConflicted"},
		{restore.ErrJournalRequired, "journalUnavailable"},
		{remote.ErrActionPartial, "partialRemoteAction"},
		{remote.ErrActionFailed, "remoteActionFailed"},
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()

			wrapped := oops.In("cli").Wrap(errors.Join(tc.cause, errSensitiveRaw))
			assertWorkflowErrorEnvelope(t, wrapped, tc.code)

			if !errors.Is(wrapped, tc.cause) {
				t.Fatal("rendering lost the original cause")
			}
		})
	}
}

func TestRestoreUncertaintySurvivesJoinedCancellationAndExitCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cause error
		code  string
	}{
		{restore.ErrExecutionUncertain, "operationUncertain"},
		{restore.ErrPriorUncertainty, "priorEffectUncertain"},
		{store.ErrUncertain, "publicationUncertain"},
		{remote.ErrActionUncertain, "remoteOutcomeUncertain"},
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()

			joined := errors.Join(context.Canceled, store.ErrConflict, tc.cause, errSensitiveRaw)
			wrapped := NewExitError(exitCodeCancel, oops.In("cli").Wrap(joined))
			assertWorkflowErrorEnvelope(t, wrapped, tc.code)
		})
	}
}

func assertWorkflowErrorEnvelope(t *testing.T, err error, code string) {
	t.Helper()

	cmd := cmdsnapshot.NewActionRestoreCommand()

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if flagErr := cmd.Flags().Set(jsonFlagName, "true"); flagErr != nil {
		t.Fatal(flagErr)
	}

	if renderErr := WriteCommandError(cmd, err); renderErr != nil {
		t.Fatal(renderErr)
	}

	if stdout.Len() != 0 {
		t.Fatalf("failure wrote stdout: %q", stdout.String())
	}

	var envelope jsonEnvelope
	if decodeErr := json.Unmarshal(stderr.Bytes(), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}

	if envelope.OK || envelope.Result != nil || envelope.Error == nil {
		t.Fatalf("invalid failure envelope: %s", stderr.String())
	}

	if envelope.Error.Code != code || envelope.Error.ExitCode != exitCodeGeneral {
		t.Fatalf("classification = %+v, want %s and exit 1", envelope.Error, code)
	}

	if envelope.Error.Message == "operation failed" || strings.Contains(stderr.String(), "/private/") {
		t.Fatalf("missing safe domain message: %s", stderr.String())
	}
}
