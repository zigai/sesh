//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/strictjson"
)

func (s *Service) checkHistory(_ context.Context) Result {
	if s.historyPath == "" {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusWarning,
			Reason:  ReasonNotConfigured,
			Message: "history state path not configured",
			Fix:     "Configure [paths].history_file",
		}
	}

	info, lstatErr := os.Lstat(s.historyPath)
	if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusError,
			Reason:  ReasonUnsafe,
			Message: "history state path has unsafe permissions or is a symlink",
			Fix:     "Ensure history state file and directory permissions are secure and not symlinked",
		}
	}

	if lstatErr != nil {
		if errors.Is(lstatErr, os.ErrNotExist) {
			return Result{
				Check:   CheckHistoryState,
				Status:  StatusWarning,
				Reason:  ReasonNotFound,
				Message: "history state file does not exist (first-run clean state)",
				Fix:     "",
			}
		}

		if errors.Is(lstatErr, os.ErrPermission) {
			return Result{
				Check:   CheckHistoryState,
				Status:  StatusError,
				Reason:  ReasonUnsafe,
				Message: "permission denied reading history state path",
				Fix:     "Fix permissions for history state file",
			}
		}
	}

	state, loadErr := session.LoadState(s.historyPath)
	if loadErr != nil {
		return classifyHistoryLoadError(loadErr)
	}

	return Result{
		Check:   CheckHistoryState,
		Status:  StatusOK,
		Reason:  "",
		Message: fmt.Sprintf("history state verified (%d switch entries, %d remote entries, %d agent view entries)", len(state.SwitchHistory), len(state.RemoteSwitchHistory), len(state.AgentViewHistory)),
		Fix:     "",
	}
}

func classifyHistoryLoadError(err error) Result {
	if errors.Is(err, session.ErrHistoryOversized) {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusError,
			Reason:  ReasonCorrupt,
			Message: "history state file exceeds maximum size limit (1 MiB)",
			Fix:     "Reset or rotate history state file",
		}
	}

	if errors.Is(err, privatefs.ErrUnsafe) || errors.Is(err, syscall.ELOOP) {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusError,
			Reason:  ReasonUnsafe,
			Message: "history state path has unsafe permissions or is a symlink",
			Fix:     "Ensure history state file and directory permissions are secure and not symlinked",
		}
	}

	if errors.Is(err, strictjson.ErrInvalid) {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusError,
			Reason:  ReasonCorrupt,
			Message: "history state contains corrupt or invalid JSON",
			Fix:     "Reset or repair history state file",
		}
	}

	if errors.Is(err, session.ErrInvalidHistoryVersion) {
		return Result{
			Check:   CheckHistoryState,
			Status:  StatusError,
			Reason:  ReasonIncompatible,
			Message: "history state has unsupported schema version",
			Fix:     "Reset or migrate history state file",
		}
	}

	return Result{
		Check:   CheckHistoryState,
		Status:  StatusError,
		Reason:  ReasonInspectionFailed,
		Message: "failed to load history state",
		Fix:     "Verify history state file permissions and contents",
	}
}
