package autosave

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const autosaveReasonRestorePending = "restore_pending"

// pendingRestore is a live tmux server that started after the newest snapshot and still lacks
// some of its saved sessions.
type pendingRestore struct {
	sessions      int
	serverStarted time.Time
}

// findPendingRestore reports a pending restore for an automatic save of every live session.
// Saving then would replace the newest snapshot with the new server's scratch state. An explicit
// session selection is a deliberate save and is never held back. When the server, the snapshot or
// the live sessions cannot be read, it reports nothing and the save goes ahead as before.
func findPendingRestore(ctx context.Context, cmd *cobra.Command, server *gotmux.Server, repo *store.Repository) pendingRestore {
	if explicit, _ := cmd.Flags().GetStringSlice("session"); len(explicit) > 0 {
		return pendingRestore{sessions: 0, serverStarted: time.Time{}}
	}

	unrestored := engine.FindUnrestored(ctx, server, repo)

	return pendingRestore{sessions: len(unrestored.Sessions), serverStarted: unrestored.ServerStarted}
}

// reportPendingRestore warns once per tmux server start that saves are held back until the saved
// sessions are restored or an explicit save keeps the live state. Without a history path, or when
// the marker cannot be read or written, it stays silent rather than warn on every interval.
func reportPendingRestore(ctx context.Context, env autosaveEnv, pending pendingRestore) {
	if env.historyPath == "" {
		return
	}

	markerPath := automation.RestorePendingMarkerPath(env.historyPath)
	stamp := pending.serverStarted.Unix()

	unreported, err := automation.EpisodeUnreported(markerPath, stamp)
	if err != nil || !unreported {
		return
	}

	if err := automation.RecordEpisodeReported(markerPath, stamp); err != nil {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", autosaveStatusSkipped).
		Str("reason", autosaveReasonRestorePending).
		Int("sessions", pending.sessions).
		Msg("tmux started after the newest snapshot and its saved sessions are not restored")
}
