//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// checkActiveRestores reports restores still recorded as active. One whose tmux server may still
// run holds back the shutdown save coordinator and startup restores until it finishes or is
// abandoned. One whose server is gone is abandoned automatically by the next of either, so it is
// only noted. It reports nothing when no restore is active or the repository cannot be read.
func (s *Service) checkActiveRestores(ctx context.Context) []Result {
	if s.snapshotDir == "" {
		return nil
	}

	repo, err := store.OpenExisting(ctx, s.snapshotDir)
	if err != nil {
		return nil
	}

	active, err := engine.ActiveRestores(ctx, repo)
	if closeErr := repo.Close(); err != nil || closeErr != nil || len(active) == 0 {
		return nil
	}

	var blocking, dead []string

	for _, op := range active {
		if op.TargetDaemonGone(ctx) {
			dead = append(dead, op.OperationID)
		} else {
			blocking = append(blocking, op.OperationID)
		}
	}

	if len(blocking) > 0 {
		return []Result{{
			Check:  CheckActiveRestores,
			Status: StatusError,
			Reason: ReasonRecoveryRequired,
			Message: fmt.Sprintf("%d restores are still marked in progress (%s), which holds back the shutdown save coordinator and startup restores",
				len(blocking), strings.Join(blocking, ", ")),
			Fix: "If no restore is running, abandon them: " + abandonCommands(blocking),
		}}
	}

	return []Result{{
		Check:  CheckActiveRestores,
		Status: StatusWarning,
		Reason: ReasonStale,
		Message: fmt.Sprintf("%d interrupted restores target a tmux server that no longer exists (%s); "+
			"the coordinator and the next startup restore abandon them automatically", len(dead), strings.Join(dead, ", ")),
		Fix: "Or abandon them now: " + abandonCommands(dead),
	}}
}

func abandonCommands(ids []string) string {
	commands := make([]string, 0, len(ids))
	for _, id := range ids {
		commands = append(commands, "sesh snapshot recover restore abandon "+id)
	}

	return strings.Join(commands, " && ")
}
