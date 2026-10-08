package tui

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/rs/zerolog"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
)

const (
	tuiOperationLoadCatalog             tuiLogOperation = "load_catalog"
	tuiOperationLoadRemoteCatalog       tuiLogOperation = "load_remote_catalog"
	tuiOperationLoadOriginSessions      tuiLogOperation = "load_origin_sessions"
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
	tuiOperationCustomFieldRun          tuiLogOperation = "custom_field_run"
)

// tuiOperationFirstFrame names the once-per-popup timing record, which has its own schema
// outside the closed set of operation outcomes.
const tuiOperationFirstFrame = "first_frame"

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
	tuiErrorTimedOut             = "timed_out"
	tuiErrorNotFound             = "not_found"
	tuiErrorNoServer             = "no_server"
	tuiErrorUnsupported          = "unsupported"
)

type tuiLogOperation string

// firstFrameTiming breaks down how long a popup took to show its first frame.
type firstFrameTiming struct {
	load        time.Duration
	loaders     *sessionLoadTiming
	sinceLaunch time.Duration
	launched    bool
}

type tuiLogOutcome uint8

func knownTUIOperation(operation tuiLogOperation) bool {
	switch operation {
	case tuiOperationLoadCatalog,
		tuiOperationLoadRemoteCatalog,
		tuiOperationLoadOriginSessions,
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
		tuiOperationAgentGitScan,
		tuiOperationCustomFieldRun:
		return true
	default:
		return false
	}
}

func classifyTUIErrorCode(err error) string {
	if err == nil {
		return tuiErrorOperationFailed
	}

	if code, ok := sentinelErrorCode(err); ok {
		return code
	}

	if outcome, ok := operationOutcome(err); ok && (outcome.Effect == gotmux.EffectUnspecified || outcome.Effect == gotmux.EffectUnknown) {
		return tuiErrorUncertainOutcome
	}

	return tuiErrorOperationFailed
}

func sentinelErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return tuiErrorTimedOut, true
	case errors.Is(err, context.Canceled):
		return tuiErrorCanceled, true
	}

	if code, ok := sessionErrorCode(err); ok {
		return code, true
	}

	if errors.Is(err, os.ErrNotExist) {
		return tuiErrorNotFound, true
	}

	return tmuxErrorCode(err)
}

func sessionErrorCode(err error) (string, bool) {
	switch {
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
	default:
		return "", false
	}
}

func tmuxErrorCode(err error) (string, bool) {
	switch {
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

	return gotmux.Outcome{Effect: gotmux.EffectNotSent, Steps: nil, Created: nil}, false
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

	event.Msg("tui operation failed")
}

func logTUIDegradation(ctx context.Context, operation tuiLogOperation, err error) {
	if !knownTUIOperation(operation) {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "tui").
		Str("operation", string(operation)).
		Str("status", "degraded").
		Str("reason", "dependency_unavailable").
		Str("error_code", classifyTUIErrorCode(err)).
		Msg("tui dependency degraded")
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
		logTUIDegradation(ctx, operation, nil)
	case tuiLogOutcomeRecovery:
		logger.Info().
			Str("component", "tui").
			Str("operation", string(operation)).
			Str("status", "recovered").
			Str("reason", "dependency_available").
			Msg("tui dependency recovered")
	}
}

func logTUIFirstFrame(ctx context.Context, timing firstFrameTiming) {
	event := zerolog.Ctx(ctx).Info().
		Str("component", "tui").
		Str("operation", tuiOperationFirstFrame).
		Str("status", "ok").
		Int64("load_ms", timing.load.Milliseconds())

	if timing.loaders != nil {
		event.Int64("catalog_ms", timing.loaders.catalog.Milliseconds()).
			Int64("agents_ms", timing.loaders.agents.Milliseconds())
	}

	if timing.launched {
		event.Int64("since_launch_ms", timing.sinceLaunch.Milliseconds())
	}

	event.Msg("tui first frame shown")
}

// firstFrameTimingAt measures a first frame shown at now. A zero initializedAt or
// launchedAt leaves that span unknown.
func firstFrameTimingAt(initializedAt, launchedAt time.Time, load *sessionLoadTiming, now time.Time) firstFrameTiming {
	timing := firstFrameTiming{load: 0, loaders: load, sinceLaunch: 0, launched: false}
	if !initializedAt.IsZero() {
		timing.load = now.Sub(initializedAt)
	}

	if !launchedAt.IsZero() && !launchedAt.After(now) {
		timing.sinceLaunch = now.Sub(launchedAt)
		timing.launched = true
	}

	return timing
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
		switch failureOutcome {
		case tuiLogOutcomeFailure:
			logTUIFailure(ctx, operation, current)
		case tuiLogOutcomeDegradation:
			logTUIDegradation(ctx, operation, current)
		case tuiLogOutcomeSuccess, tuiLogOutcomeRecovery:
			logTUIEvent(ctx, operation, failureOutcome)
		}
	case previous != nil && current == nil:
		logTUIEvent(ctx, operation, tuiLogOutcomeRecovery)
	}
}
