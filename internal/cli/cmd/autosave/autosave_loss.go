package autosave

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// reportRestorableSessions says, once per autosave episode, that tmux is gone while the newest
// snapshot still holds sessions. It returns the session count when it reported, so the caller can
// show it, and zero in every silent case: no recorded autosave to follow, an already reported
// loss, nothing saved, or marker state that cannot be read or written.
func reportRestorableSessions(ctx context.Context, env autosaveEnv, repo *store.Repository) int {
	if env.historyPath == "" || env.lastSavedUnix <= 0 {
		return 0
	}

	markerPath := automation.ServerLossMarkerPath(env.historyPath)

	unreported, err := automation.ServerLossUnreported(markerPath, env.lastSavedUnix)
	if err != nil || !unreported {
		return 0
	}

	count := engine.RestorableSessionCount(ctx, repo)
	if count == 0 {
		return 0
	}

	if err := automation.RecordServerLossReported(markerPath, env.lastSavedUnix); err != nil {
		return 0
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", autosaveStatusSkipped).
		Str("reason", autosaveReasonRestorableSessions).
		Int("sessions", count).
		Msg("tmux is not running and a snapshot has restorable sessions")

	return count
}
