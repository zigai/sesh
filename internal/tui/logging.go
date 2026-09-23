package tui

import (
	"context"
	"errors"
	"os"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
)

const (
	tuiOperationLoadCatalog             tuiLogOperation = "load_catalog"
	tuiOperationLoadRemoteCatalog       tuiLogOperation = "load_remote_catalog"
	tuiOperationLoadAgentSummaries      tuiLogOperation = "load_agent_summaries"
	tuiOperationEnterLiveSession        tuiLogOperation = "enter_live_session"
	tuiOperationRestoreSavedSession     tuiLogOperation = "restore_saved_session"
	tuiOperationEnterOrCreateSession    tuiLogOperation = "enter_or_create_session"
	tuiOperationActivateAgentPane       tuiLogOperation = "activate_agent_pane"
	tuiOperationRenameSession           tuiLogOperation = "rename_session"
	tuiOperationKillSession             tuiLogOperation = "kill_session"
	tuiOperationDeleteSession           tuiLogOperation = "delete_session"
	tuiOperationResolveWorkingDirectory tuiLogOperation = "resolve_working_directory"
	tuiOperationSaveState               tuiLogOperation = "save_state"
	tuiOperationSnapshotSession         tuiLogOperation = "snapshot_session"
	tuiOperationAgentGitScan            tuiLogOperation = "agent_git_scan"
)

const (
	tuiLogOutcomeSuccess tuiLogOutcome = iota
	tuiLogOutcomeFailure
	tuiLogOutcomeDegradation
	tuiLogOutcomeRecovery
)

const (
	tuiErrorOperationFailed      = "operation_failed"
	tuiErrorSessionAttached      = "session_attached"
	tuiErrorSessionAlreadyExists = "session_already_exists"
	tuiErrorSessionNotFound      = "session_not_found"
	tuiErrorInvalidSessionName   = "invalid_session_name"
	tuiErrorUncertainOutcome     = "uncertain_outcome"
	tuiErrorCanceled             = "canceled"
	tuiErrorNotFound             = "not_found"
	tuiErrorNoServer             = "no_server"
	tuiErrorUnsupported          = "unsupported"
)

type tuiLogOperation string

type tuiLogOutcome uint8

func knownTUIOperation(operation tuiLogOperation) bool {
	switch operation {
	case tuiOperationLoadCatalog,
		tuiOperationLoadRemoteCatalog,
		tuiOperationLoadAgentSummaries,
		tuiOperationEnterLiveSession,
		tuiOperationRestoreSavedSession,
		tuiOperationEnterOrCreateSession,
		tuiOperationActivateAgentPane,
		tuiOperationRenameSession,
		tuiOperationKillSession,
		tuiOperationDeleteSession,
		tuiOperationResolveWorkingDirectory,
		tuiOperationSaveState,
		tuiOperationSnapshotSession,
		tuiOperationAgentGitScan:
		return true
	default:
		return false
	}
}

func classifyTUIErrorCode(err error) string {
	if err == nil {
		return tuiErrorOperationFailed
	}

	if oopsErr, ok := oops.AsOops(err); ok {
		if code, ok := oopsErr.Code().(string); ok && code != "" {
			return code
		}
	}

	if code, ok := sentinelErrorCode(err); ok {
		return code
	}

	if outcome, ok := operationOutcome(err); ok && outcome.Effect == gotmux.Unknown {
		return tuiErrorUncertainOutcome
	}

	return tuiErrorOperationFailed
}

func sentinelErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return tuiErrorCanceled, true
	case errors.Is(err, session.ErrSessionAttached):
		return tuiErrorSessionAttached, true
	case errors.Is(err, session.ErrSessionAlreadyExists):
		return tuiErrorSessionAlreadyExists, true
	case errors.Is(err, session.ErrSessionNotFound):
		return tuiErrorSessionNotFound, true
	case errors.Is(err, session.ErrInvalidSessionName):
		return tuiErrorInvalidSessionName, true
	case errors.Is(err, session.ErrUncertain):
		return tuiErrorUncertainOutcome, true
	case errors.Is(err, os.ErrNotExist):
		return tuiErrorNotFound, true
	case errors.Is(err, gotmux.ErrNoServer):
		return tuiErrorNoServer, true
	case errors.Is(err, gotmux.ErrNotFound):
		return tuiErrorSessionNotFound, true
	case errors.Is(err, gotmux.ErrUnsupported), errors.Is(err, gotmux.ErrTransportUnsupported):
		return tuiErrorUnsupported, true
	default:
		return "", false
	}
}

func operationOutcome(err error) (gotmux.Outcome, bool) {
	operationErr, ok := errors.AsType[*gotmux.OperationError](err)
	if ok {
		return operationErr.Outcome, true
	}

	return gotmux.Outcome{Effect: gotmux.NotSent, Steps: nil, Created: nil}, false
}

func logTUIFailure(ctx context.Context, operation tuiLogOperation, err error) {
	if !knownTUIOperation(operation) {
		return
	}

	logger := zerolog.Ctx(ctx)

	event := logger.Error().
		Str("component", "tui").
		Str("operation", string(operation)).
		Str("status", "failed").
		Str("error_code", classifyTUIErrorCode(err))

	if oopsErr, ok := oops.AsOops(err); ok {
		contextMap := oopsErr.Context()
		if phase, ok := contextMap["phase"].(string); ok && phase != "" {
			event.Str("phase", phase)
		}

		if action, ok := contextMap["action"].(string); ok && action != "" {
			event.Str("action", action)
		}
	}

	event.Msg("tui operation failed")
}

func logTUIEvent(ctx context.Context, operation tuiLogOperation, outcome tuiLogOutcome) {
	if !knownTUIOperation(operation) {
		return
	}

	logger := zerolog.Ctx(ctx)

	switch outcome {
	case tuiLogOutcomeSuccess:
		logger.Info().
			Str("component", "tui").
			Str("operation", string(operation)).
			Str("status", "ok").
			Msg("tui operation completed")
	case tuiLogOutcomeFailure:
		logTUIFailure(ctx, operation, nil)
	case tuiLogOutcomeDegradation:
		logger.Warn().
			Str("component", "tui").
			Str("operation", string(operation)).
			Str("status", "degraded").
			Msg("tui dependency degraded")
	case tuiLogOutcomeRecovery:
		logger.Info().
			Str("component", "tui").
			Str("operation", string(operation)).
			Str("status", "recovered").
			Msg("tui dependency recovered")
	}
}

func logTUILoadTransition(
	ctx context.Context,
	operation tuiLogOperation,
	previous error,
	current error,
	failureOutcome tuiLogOutcome,
) {
	switch {
	case previous == nil && current != nil:
		if failureOutcome == tuiLogOutcomeFailure {
			logTUIFailure(ctx, operation, current)
		} else {
			logTUIEvent(ctx, operation, failureOutcome)
		}
	case previous != nil && current == nil:
		logTUIEvent(ctx, operation, tuiLogOutcomeRecovery)
	}
}
