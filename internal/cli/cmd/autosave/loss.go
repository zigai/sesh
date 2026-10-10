package autosave

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// reportRestorableSessions reports once per episode that tmux is gone but the newest snapshot
// has sessions. It returns the session count, or zero when nothing was reported.
func reportRestorableSessions(ctx context.Context, env Env, repo *store.Repository) int {
	if env.workspace.History == "" || env.lastSavedUnix <= 0 {
		return 0
	}

	markerPath := automation.ServerLossMarkerPath(env.workspace.History)

	unreported, err := automation.EpisodeUnreported(markerPath, env.lastSavedUnix)
	if err != nil || !unreported {
		return 0
	}

	count := engine.RestorableSessionCount(ctx, repo)
	if count == 0 {
		return 0
	}

	if err := automation.RecordEpisodeReported(markerPath, env.lastSavedUnix); err != nil {
		return 0
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", statusSkipped).
		Str("reason", reasonRestorableSessions).
		Int("sessions", count).
		Msg("tmux is not running and a snapshot has restorable sessions")

	return count
}
