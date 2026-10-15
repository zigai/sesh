//go:build linux || darwin

package doctor

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/zigai/sesh/internal/setup"
)

const daemonPhaseRunning = "running"

func (s *Service) checkDaemon() Result {
	if runtime.GOOS != setup.PlatformLinux {
		return newResult(CheckAutosaveDaemon, StatusSkip, "The save coordinator runs only on Linux")
	}

	daemon := s.opts.Daemon
	unit := setup.CoordinatorServiceName
	status := "systemctl --user status " + unit

	switch daemon.State {
	case DaemonReady:
		if daemon.Phase == daemonPhaseRunning && daemon.InhibitorsHeld {
			return newResult(CheckAutosaveDaemon, StatusOK, "Coordinator runs and holds its shutdown and sleep inhibitors")
		}

		return s.busyDaemon(status)
	case DaemonNotReady:
		return s.busyDaemon(status)
	case DaemonStale:
		return newResult(CheckAutosaveDaemon, StatusWarn, "Runtime socket "+DisplayPath(daemon.RuntimePath)+" has no live owner").
			withReason(ReasonStale).
			withCommand("rm " + shellPath(daemon.RuntimePath))
	case DaemonAbsent:
		return s.absentDaemon(unit)
	default:
		return newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator state at "+DisplayPath(daemon.RuntimePath)+" could not be verified").
			withReason(ReasonUnverifiable).
			withCommand(status)
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

func (s *Service) absentDaemon(unit string) Result {
	enable := "systemctl --user enable --now " + unit

	switch {
	case s.unitInstalled(unit):
		return newResult(CheckAutosaveDaemon, StatusWarn, unit+" is installed but not running").
			withReason(ReasonNotRunning).
			withCommand(enable)
	case s.opts.AutosaveEnabled:
		return newResult(CheckAutosaveDaemon, StatusWarn, "Coordinator is not installed, so no save runs before shutdown or sleep").
			withReason(ReasonNotRunning).
			withCommand(s.seshCommand("setup services --apply --with-coordinator", []string{flagSocket, flagRepository, flagHistory}) + " && " + enable)
	default:
		return newResult(CheckAutosaveDaemon, StatusInfo, "Coordinator is not running; autosave is off").withReason(ReasonNotRunning)
	}
}

func (s *Service) unitInstalled(unit string) bool {
	for _, dir := range s.opts.ServiceDirs {
		if _, err := os.Stat(filepath.Join(dir, unit)); err == nil {
			return true
		}
	}

	return false
}
