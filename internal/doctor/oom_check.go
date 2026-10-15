//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"
)

const oomPolicyStop = "stop"

// checkServiceOOMPolicy warns when the service that owns the tmux server stops the whole unit
// after the kernel kills any one process in it, which turns one runaway pane process into the
// loss of the server and every pane.
func (s *Service) checkServiceOOMPolicy(ctx context.Context) Result {
	if s.opts.ServiceDirs == nil {
		return newResult(CheckServiceOOMPolicy, StatusSkip, "Only systemd services are checked")
	}

	if s.serverPID <= 0 {
		return skipped(CheckServiceOOMPolicy, CheckTmuxServer, "tmux server is unavailable")
	}

	unit, err := s.unitOf(s.serverPID)
	if err != nil || unit == "" {
		return newResult(CheckServiceOOMPolicy, StatusSkip, "tmux server does not run in a systemd service")
	}

	policy, err := s.oomPolicyOf(ctx, unit)
	if err != nil {
		return newResult(CheckServiceOOMPolicy, StatusSkip, fmt.Sprintf("OOMPolicy of %s is unreadable: %v", unit, err))
	}

	if policy != oomPolicyStop {
		return newResult(CheckServiceOOMPolicy, StatusOK, fmt.Sprintf("%s uses OOMPolicy=%s", unit, policy))
	}

	return newResult(CheckServiceOOMPolicy, StatusWarn,
		unit+" uses OOMPolicy=stop: one process killed for memory stops the unit, including the tmux server and every pane").
		withReason(ReasonUnsafe).
		withManual(fmt.Sprintf("Set OOMPolicy=continue in the [Service] section of %s (systemctl --user edit %s), then restart tmux at a convenient time", unit, unit))
}
