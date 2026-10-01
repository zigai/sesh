//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// checkRestorable adds a result only when no tmux server is reachable while the newest snapshot
// still holds sessions, so a tmux server that died is not mistaken for sessions that never were.
func (s *Service) checkRestorable(ctx context.Context, tmuxRes Result) []Result {
	if tmuxRes.Status != StatusError || tmuxRes.Reason != ReasonUnavailable || s.snapshotDir == "" {
		return nil
	}

	repo, err := store.OpenExisting(ctx, s.snapshotDir)
	if err != nil {
		return nil
	}

	count := engine.RestorableSessionCount(ctx, repo)

	if closeErr := repo.Close(); closeErr != nil || count == 0 {
		return nil
	}

	return []Result{{
		Check:   CheckRestorableSnapshot,
		Status:  StatusWarning,
		Reason:  ReasonNotRunning,
		Message: fmt.Sprintf("no tmux server is running and the newest snapshot holds %d sessions", count),
		Fix:     "Start tmux on the selected endpoint, then run 'sesh snapshot restore'",
	}}
}
