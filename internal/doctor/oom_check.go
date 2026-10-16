//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

const oomPolicyStop = "stop"

var errNoServiceUnit = errors.New("process is not in a service unit cgroup")

// checkServiceOOMPolicy warns when the service that owns the tmux server stops the whole unit
// after the kernel kills any one process in it, which turns one runaway pane process into the
// loss of the server and every pane.
func (s *Service) checkServiceOOMPolicy(ctx context.Context) []Result {
	if runtime.GOOS != "linux" || s.opts.ServiceDirs == nil {
		return nil
	}

	if s.serverPID <= 0 {
		return []Result{skipped(CheckServiceOOMPolicy, CheckTmuxServer, "tmux server is unavailable")}
	}

	unit, err := s.unitOf(s.serverPID)
	if errors.Is(err, errNoServiceUnit) {
		return nil
	}

	if err != nil || unit == "" {
		message := "tmux server service membership could not be inspected: service unit was not returned"
		if err != nil {
			message = fmt.Sprintf("tmux server service membership could not be inspected: %v", err)
		}

		return []Result{newResult(CheckServiceOOMPolicy, StatusWarn, message).
			withReason(ReasonInspectionFailed).
			withManual("Check that the tmux server process cgroup is readable, then run 'sesh doctor' again")}
	}

	policy, err := s.oomPolicyOf(ctx, unit)
	if err != nil {
		return []Result{newResult(CheckServiceOOMPolicy, StatusWarn, fmt.Sprintf("OOMPolicy of %s is unreadable: %v", unit, err)).
			withReason(ReasonInspectionFailed).
			withCommand("systemctl --user show --property=OOMPolicy " + shellArg(unit))}
	}

	if policy != oomPolicyStop {
		return []Result{newResult(CheckServiceOOMPolicy, StatusOK, fmt.Sprintf("%s uses OOMPolicy=%s", unit, policy))}
	}

	return []Result{newResult(CheckServiceOOMPolicy, StatusWarn,
		unit+" uses OOMPolicy=stop: one process killed for memory stops the unit, including the tmux server and every pane").
		withReason(ReasonUnsafe).
		withManual(fmt.Sprintf("Set OOMPolicy=continue in the [Service] section of %s (systemctl --user edit %s), then restart tmux at a convenient time", unit, unit))}
}
