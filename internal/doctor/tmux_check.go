//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	minTmuxMajor = 3
	minTmuxMinor = 6
)

// checkTmux also returns the server pid when one answered, for checks that follow the server.
func (s *Service) checkTmux(ctx context.Context) (Result, int) {
	if s.server == nil {
		return Result{
			Check:   CheckTmux,
			Status:  StatusWarning,
			Reason:  ReasonNotConfigured,
			Message: "no explicit tmux server endpoint selected",
			Fix:     "Specify an explicit tmux server endpoint",
		}, 0
	}

	info, err := s.server.Probe(ctx)
	if err != nil {
		return classifyTmuxProbeError(err), 0
	}

	if !info.Version.AtLeast(minTmuxMajor, minTmuxMinor) {
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonIncompatible,
			Message: fmt.Sprintf("tmux version %s is unsupported (requires 3.6+)", info.Version.String()),
			Fix:     "Upgrade tmux to 3.6 or later",
		}, info.Identity.PID
	}

	return Result{
		Check:   CheckTmux,
		Status:  StatusOK,
		Reason:  "",
		Message: fmt.Sprintf("tmux %s (server pid %d)", info.Version.String(), info.Identity.PID),
		Fix:     "",
	}, info.Identity.PID
}

func classifyTmuxProbeError(err error) Result {
	switch {
	case errors.Is(err, context.Canceled):
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonUnavailable,
			Message: "tmux server probe was canceled",
			Fix:     "",
		}
	case errors.Is(err, context.DeadlineExceeded):
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonUnavailable,
			Message: "tmux server probe timed out",
			Fix:     "",
		}
	case errors.Is(err, gotmux.ErrNoServer):
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonUnavailable,
			Message: "no running tmux daemon found on selected endpoint",
			Fix:     "Start tmux on the target endpoint or specify an active socket",
		}
	case errors.Is(err, os.ErrPermission):
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonUnsafe,
			Message: "permission denied accessing tmux server socket",
			Fix:     "Check socket permissions and ownership",
		}
	}

	if unsupportedErr, ok := errors.AsType[*gotmux.UnsupportedError](err); ok {
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonIncompatible,
			Message: fmt.Sprintf("tmux version %s is unsupported (requires 3.6+)", unsupportedErr.Version.String()),
			Fix:     "Upgrade tmux to 3.6 or later",
		}
	}

	if isExecutableNotFound(err) {
		return Result{
			Check:   CheckTmux,
			Status:  StatusError,
			Reason:  ReasonUnavailable,
			Message: "tmux executable not found in PATH",
			Fix:     "Install tmux or add it to PATH",
		}
	}

	return Result{
		Check:   CheckTmux,
		Status:  StatusError,
		Reason:  ReasonInspectionFailed,
		Message: "failed to communicate with tmux server",
		Fix:     "Check tmux server endpoint and system status",
	}
}

func isExecutableNotFound(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}

	var execErr *exec.Error

	return errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound)
}
