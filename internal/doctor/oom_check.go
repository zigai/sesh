//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"
)

const oomPolicyStop = "stop"

// checkServiceOOMPolicy warns when the service that owns the tmux server stops the whole unit
// after the kernel kills any one process in it, which turns one runaway pane process into the
// loss of the server and every pane. It stays silent when the owning unit cannot be determined.
func (s *Service) checkServiceOOMPolicy(ctx context.Context, serverPID int) []Result {
	if serverPID <= 0 || s.unitOf == nil || s.oomPolicyOf == nil {
		return nil
	}

	unit, err := s.unitOf(serverPID)
	if err != nil || unit == "" {
		return nil
	}

	policy, err := s.oomPolicyOf(ctx, unit)
	if err != nil || policy != oomPolicyStop {
		return nil
	}

	return []Result{{
		Check:   CheckServiceOOMPolicy,
		Status:  StatusWarning,
		Reason:  ReasonUnsafe,
		Message: unit + " uses OOMPolicy=stop: one process killed for memory stops the unit, including the tmux server and every pane",
		Fix:     fmt.Sprintf("Set OOMPolicy=continue in the [Service] section of %s (systemctl --user edit %s), then restart tmux at a convenient time", unit, unit),
	}}
}
