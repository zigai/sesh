//go:build linux || darwin

package app

import (
	"context"
	"errors"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/task"
)

// classifications map domain sentinels to action codes. The first match wins, so uncertain
// outcomes are checked before the failures that may accompany them.
var classifications = []classification{
	{context.Canceled, action.CodeCanceled},
	{store.ErrUncertain, action.CodeUncertain},
	{session.ErrUncertain, action.CodeUncertain},
	{restore.ErrExecutionUncertain, action.CodeUncertain},
	{restore.ErrPriorUncertainty, action.CodeUncertain},
	{session.ErrSwitchUnverified, action.CodeUncertain},
	{task.ErrUncertain, action.CodeUncertain},
	{session.ErrSessionNotFound, action.CodeNotFound},
	{session.ErrSessionAlreadyExists, action.CodeAlreadyExists},
	{restore.ErrSessionCollision, action.CodeAlreadyExists},
	{restore.ErrGroupCollision, action.CodeAlreadyExists},
	{session.ErrSessionAttached, action.CodeAttached},
	{session.ErrSessionReplaced, action.CodeTargetChanged},
	{session.ErrServerIdentityChanged, action.CodeTargetChanged},
	{capture.ErrChanged, action.CodeTargetChanged},
	{gotmux.ErrServerChanged, action.CodeTargetChanged},
	{session.ErrInvalidSessionName, action.CodeInvalidArgument},
	{capture.ErrSelection, action.CodeInvalidArgument},
	{session.ErrSavedGenerationsProtected, action.CodeConflict},
	{session.ErrSavedSessionScopeConflict, action.CodeConflict},
	{store.ErrConflict, action.CodeConflict},
	{store.ErrRecoveryRequired, action.CodeConflict},
	{restore.ErrPriorConflict, action.CodeConflict},
	{session.ErrNotInsideTmux, action.CodeUnavailable},
	{session.ErrInvokingClientUnavailable, action.CodeUnavailable},
	{session.ErrInvokingClientAmbiguous, action.CodeUnavailable},
	{session.ErrTerminalRequired, action.CodeUnavailable},
	{store.ErrUnavailable, action.CodeUnavailable},
	{agents.ErrCurrentContextUnknown, action.CodeUnavailable},
	{agents.ErrCurrentPaneNotLive, action.CodeUnavailable},
	{agents.ErrRegistryUnavailable, action.CodeUnavailable},
	{agents.ErrNoTargetPane, action.CodeNotFound},
	{agents.ErrEmptyNavigationHistory, action.CodeNotFound},
	{agents.ErrPaneNotLive, action.CodeNotFound},
	{gotmux.ErrNoServer, action.CodeUnavailable},
	{engine.ErrNoServerRunning, action.CodeUnavailable},
	{gotmux.ErrNotFound, action.CodeNotFound},
	{task.ErrUnknownHarness, action.CodeInvalidArgument},
	{task.ErrNoBaseBranch, action.CodeConflict},
	{task.ErrNoFreeNumber, action.CodeConflict},
	{task.ErrPaneNotFound, action.CodeNotFound},
	{task.ErrNotFound, action.CodeNotFound},
	{task.ErrWindowMissing, action.CodeNotFound},
	{task.ErrNotAwake, action.CodeConflict},
	{task.ErrNotAsleep, action.CodeConflict},
	{task.ErrNoAgent, action.CodeNotFound},
	{task.ErrAgentBusy, action.CodeConflict},
	{task.ErrNoResume, action.CodeUnsupported},
	{task.ErrSleepUnsupported, action.CodeUnsupported},
	{task.ErrAgentChanged, action.CodeTargetChanged},
	{task.ErrAgentDidNotStop, action.CodeFailed},
	{task.ErrWouldLoseWork, action.CodeConflict},
	{task.ErrTaskStarting, action.CodeConflict},
	{task.ErrGitHubFailed, action.CodeUnavailable},
	{task.ErrGitHubResponse, action.CodeFailed},
	{ErrUnknownTaskAction, action.CodeNotFound},
	{ErrPromptFile, action.CodeInvalidArgument},
	{project.ErrNotProject, action.CodeInvalidArgument},
	{project.ErrNameInvalid, action.CodeInvalidArgument},
	{project.ErrNameTaken, action.CodeAlreadyExists},
	{project.ErrExists, action.CodeAlreadyExists},
	{project.ErrNoRoot, action.CodeUnavailable},
	{project.ErrSessionNotFound, action.CodeNotFound},
}

type classification struct {
	cause error
	code  action.Code
}

func classify(err error) action.Code {
	if classified, ok := errors.AsType[*action.Error](err); ok {
		return classified.Code
	}

	for _, entry := range classifications {
		if errors.Is(err, entry.cause) {
			return entry.code
		}
	}

	if _, ok := errors.AsType[*capture.SessionNotFoundError](err); ok {
		return action.CodeNotFound
	}

	return action.CodeFailed
}
