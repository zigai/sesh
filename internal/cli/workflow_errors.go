package cli

import (
	"errors"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	uncertainCommandErrors = []commandErrorContract{
		{store.ErrUncertain, "publicationUncertain", "snapshot publication durability uncertain"},
		{restore.ErrExecutionUncertain, "operationUncertain", "restore effect outcome uncertain"},
		{restore.ErrPriorUncertainty, "priorEffectUncertain", "previous attempt left an uncertain effect; explicit reconciliation required"},
		{remote.ErrActionUncertain, "remoteOutcomeUncertain", "remote action outcome is uncertain; reconcile the original invocation"},
	}

	workflowCommandErrors = []commandErrorContract{
		{errDoctorChecksFailed, "doctorChecksFailed", "doctor checks failed"},
		{engine.ErrRetentionBudgetUnmet, "retentionBudgetUnmet", "protected snapshot bytes exceed retention budget"},
		{engine.ErrCollectionQuarantined, "collectionQuarantined", "snapshot collection preserved contradictory evidence"},
		{gotmux.ErrOutputLimit, "outputLimitExceeded", "tmux command output exceeded its byte limit"},
		{config.ErrConfigExists, "configExists", "configuration already exists; use --force to replace it"},
		{config.ErrConfigSymlink, "configSymlink", "configuration target must not be a symbolic link"},
		{errPartialRestore, "partialRestore", "parity-incomplete restore"},
		{remote.ErrActionPartial, "partialRemoteAction", "remote action completed with incomplete recovery"},
		{remote.ErrActionFailed, "remoteActionFailed", "remote action failed"},
		{restore.ErrPriorConflict, "priorEffectConflicted", "previous restore effects conflict with recorded state; explicit reconciliation required"},
		{restore.ErrJournalRequired, "journalUnavailable", "restore journal unavailable"},
		{restore.ErrJournalIncomplete, "journalUnavailable", "restore journal unavailable"},
		{session.ErrNotInsideTmux, "notInsideTmux", "switch requires an attached tmux client"},
		{session.ErrInvokingClientUnavailable, "clientUnavailable", "no attached client displays the invoking session"},
		{session.ErrInvokingClientAmbiguous, "clientAmbiguous", "multiple attached clients display the invoking session"},
		{session.ErrSwitchUnverified, "switchUnverified", "client switch could not be verified"},
		{session.ErrTerminalRequired, "interactiveTerminalRequired", "attach requires an interactive terminal"},
		{session.ErrSessionNotFound, "notFound", "session not found"},
		{session.ErrSessionAlreadyExists, "sessionAlreadyExists", "session already exists"},
		{session.ErrSessionAttached, "sessionAttached", "session is attached"},
		{session.ErrSessionReplaced, "sessionReplaced", "session changed before the operation could finish"},
		{session.ErrSavedGenerationsProtected, "savedGenerationsProtected", "saved generations containing the session are protected"},
		{session.ErrSavedSessionScopeConflict, "savedSessionScopeConflict", "saved session source endpoint is unverified"},
		{agents.ErrRegistrySchema, "agentRegistrySchema", "agent registry schema is not supported"},
		{agents.ErrRegistryCorrupt, "agentRegistryCorrupt", "agent registry is corrupt"},
		{agents.ErrRegistryUnavailable, "agentRegistryUnavailable", "agent registry is unavailable"},
		{agents.ErrRegistryTooLarge, "agentRegistryTooLarge", "agent registry exceeds its size limit"},
		{agents.ErrStyleOwnershipCorrupt, "agentStyleOwnershipCorrupt", "agent style ownership record is corrupt"},
		{agents.ErrCurrentContextUnknown, "agentContextUnknown", "current tmux context could not be determined; pass --pane, --client, or --session"},
		{agents.ErrCurrentPaneNotLive, "agentPaneNotLive", "agent pane is not a live tmux pane"},
		{agents.ErrPaneNotLive, "agentPaneNotLive", "agent pane is not a live tmux pane"},
		{agents.ErrNoTargetPane, "agentPaneNotFound", "no agent pane needs attention"},
		{agents.ErrEmptyNavigationHistory, "agentHistoryEmpty", "no agent navigation history"},
	}
)

type commandErrorContract struct {
	cause   error
	code    string
	message string
}

func matchCommandError(err error, contracts []commandErrorContract) (commandErrorContract, bool) {
	for _, contract := range contracts {
		if errors.Is(err, contract.cause) {
			return contract, true
		}
	}

	return commandErrorContract{cause: nil, code: "", message: ""}, false
}

func commandOutcomeLogged(err error) bool {
	if err == nil {
		return false
	}

	if owner, ok := err.(interface{ OutcomeLogged() bool }); ok {
		return owner.OutcomeLogged()
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !commandOutcomeLogged(cause) {
				return false
			}
		}

		return true
	}

	return commandOutcomeLogged(errors.Unwrap(err))
}

func restoreOutcomeLogged(err error) bool {
	if err == nil {
		return true
	}

	if owner, ok := err.(interface{ RestoreOutcomeLogged() bool }); ok {
		return owner.RestoreOutcomeLogged()
	}

	if owner, ok := err.(interface{ restoreOutcomeLogged() bool }); ok {
		return owner.restoreOutcomeLogged()
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !restoreOutcomeLogged(cause) {
				return false
			}
		}

		return true
	}

	if cause := errors.Unwrap(err); cause != nil {
		return restoreOutcomeLogged(cause)
	}

	return errors.Is(err, errPartialRestore)
}
