//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zigai/sesh/internal/setup"
)

const daemonPhaseRunning = "running"

func (s *Service) checkDaemon(ctx context.Context) []Result {
	if runtime.GOOS != setup.PlatformLinux {
		return nil
	}

	daemon := s.opts.Daemon
	unit := setup.CoordinatorServiceName
	status := "systemctl --user status " + unit

	switch daemon.State {
	case DaemonReady:
		if daemon.Phase == daemonPhaseRunning && daemon.InhibitorsHeld {
			return []Result{newResult(CheckAutosaveDaemon, StatusOK, "Coordinator runs and holds its shutdown and sleep inhibitors")}
		}

		return []Result{s.busyDaemon(status)}
	case DaemonNotReady:
		return []Result{s.busyDaemon(status)}
	case DaemonStale:
		return []Result{newResult(CheckAutosaveDaemon, StatusWarn, "Runtime socket "+DisplayPath(daemon.RuntimePath)+" has no live owner").
			withReason(ReasonStale).
			withCommand("rm " + shellPath(daemon.RuntimePath))}
	case DaemonAbsent:
		return s.absentDaemon(ctx, unit)
	default:
		return []Result{newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator state at "+DisplayPath(daemon.RuntimePath)+" could not be verified").
			withReason(ReasonUnverifiable).
			withCommand(status)}
	}
}

func (s *Service) busyDaemon(status string) Result {
	switch s.opts.Daemon.Phase {
	case "initializing":
		return newResult(CheckAutosaveDaemon, StatusInfo, "Coordinator is still initializing")
	case daemonPhaseRunning:
		return newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator runs but has released its shutdown and sleep inhibitors").
			withReason(ReasonInhibitorReleased).
			withCommand(status)
	default:
		return newResult(CheckAutosaveDaemon, StatusInfo, "Coordinator is handling a shutdown or sleep event")
	}
}

func (s *Service) absentDaemon(ctx context.Context, unit string) []Result {
	enable := "systemctl --user enable --now " + unit
	installed, err := s.unitInstalled(unit)

	switch {
	case err != nil:
		return []Result{newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator unit installation could not be inspected").
			withReason(ReasonInspectionFailed).
			withDetails(err.Error()).
			withManual("Make the coordinator unit search locations readable and repair broken unit links")}
	case installed:
		return []Result{s.installedDaemon(ctx, unit, enable)}
	case s.opts.AutosaveEnabled:
		return []Result{newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator is not installed, so no save runs before shutdown or sleep").
			withReason(ReasonNotRunning).
			withCommand(s.seshCommand("setup services --apply --with-coordinator", []string{flagSocket, flagRepository, flagHistory}) + " && " + enable)}
	default:
		return nil
	}
}

func (s *Service) installedDaemon(ctx context.Context, unit, enable string) Result {
	state, err := s.opts.UnitActiveState(ctx, unit)
	if err != nil {
		return newResult(CheckAutosaveDaemon, StatusWarn, unit+" state could not be read").
			withReason(ReasonInspectionFailed).
			withDetails(err.Error()).
			withCommand("systemctl --user status " + unit)
	}

	switch state {
	case "inactive":
		return newResult(CheckAutosaveDaemon, StatusWarn, unit+" is installed but not running").
			withReason(ReasonNotRunning).
			withCommand(enable)
	case "failed":
		return newResult(CheckAutosaveDaemon, StatusWarn, unit+" failed and is not running").
			withReason(ReasonNotRunning).
			withCommand(enable)
	case "active", "reloading":
		return newResult(CheckAutosaveDaemon, StatusInfo, unit+" runs but has not published its status for this tmux server")
	default:
		return newResult(CheckAutosaveDaemon, StatusInfo, unit+" is "+state)
	}
}

func (s *Service) unitInstalled(unit string) (bool, error) {
	for _, dir := range s.opts.ServiceDirs {
		path := filepath.Join(dir, unit)

		_, err := os.Stat(path)
		if err == nil {
			return true, nil
		}

		if errors.Is(err, os.ErrNotExist) {
			if _, linkErr := os.Lstat(path); errors.Is(linkErr, os.ErrNotExist) {
				continue
			}
		}

		return false, fmt.Errorf("%s: %w", DisplayPath(path), err)
	}

	return false, nil
}
