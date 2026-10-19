package autosave

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	reasonRestorePending    = "restore_pending"
	reasonRestoreIncomplete = "restore_incomplete"
	reasonEpisodeMarker     = "episode_marker_failed"
	reasonRestoreTarget     = "restore_target_unavailable"
)

type pendingRestore struct {
	sessions      int
	serverStarted time.Time
}

func findPendingRestore(ctx context.Context, cmd *cobra.Command, server *gotmux.Server, repo *store.Repository) pendingRestore {
	// A save of explicitly selected sessions is deliberate and never held back.
	if explicit, _ := cmd.Flags().GetStringSlice("session"); len(explicit) > 0 {
		return pendingRestore{sessions: 0, serverStarted: time.Time{}}
	}

	// Saving every session now would replace the newest snapshot with the new server's scratch
	// state. An inspection failure is logged by the engine and lets the save go ahead.
	unrestored := engine.FindUnrestored(ctx, server, repo)

	return pendingRestore{sessions: len(unrestored.Sessions), serverStarted: unrestored.ServerStarted}
}

type pendingRestoreAction int

const (
	pendingRestoreHold pendingRestoreAction = iota
	pendingRestoreRun
	pendingRestoreSave
)

func decidePendingRestore(ctx context.Context, env Env, server *gotmux.Server, repo *store.Repository, pending pendingRestore) pendingRestoreAction {
	targeted, err := engine.RestoreTargetedServer(ctx, repo, server)
	if err != nil {
		logPendingRestoreFailure(ctx, reasonRestoreTarget)

		return pendingRestoreHold
	}

	// A second restore would bring back sessions closed since, and holding back would stop every
	// later save.
	if targeted {
		return pendingRestoreSave
	}

	if !env.workspace.Config.Autosave.RestoreLastOnStart {
		return pendingRestoreHold
	}

	// A marker read failure holds the save; the hold path reports it when it claims the episode.
	if unhandled, err := pendingEpisodeUnhandled(env, pending); err == nil && unhandled {
		return pendingRestoreRun
	}

	return pendingRestoreHold
}

func pendingEpisodeUnhandled(env Env, pending pendingRestore) (bool, error) {
	if env.workspace.History == "" {
		return false, nil
	}

	unhandled, err := automation.EpisodeUnreported(automation.RestorePendingMarkerPath(env.workspace.History), pending.serverStarted.Unix())
	if err != nil {
		return false, oops.In("cli").With("operation", "read_restore_pending_marker").Wrap(err)
	}

	return unhandled, nil
}

func claimPendingEpisode(env Env, pending pendingRestore) (bool, error) {
	unhandled, err := pendingEpisodeUnhandled(env, pending)
	if err != nil || !unhandled {
		return false, err
	}

	if err := automation.RecordEpisodeReported(automation.RestorePendingMarkerPath(env.workspace.History), pending.serverStarted.Unix()); err != nil {
		return false, oops.In("cli").With("operation", "record_restore_pending_marker").Wrap(err)
	}

	return true, nil
}

func claimPendingReport(ctx context.Context, env Env, pending pendingRestore) bool {
	claimed, err := claimPendingEpisode(env, pending)
	if err != nil {
		logPendingRestoreFailure(ctx, reasonEpisodeMarker)
	}

	return claimed
}

func reportRestoreIncomplete(ctx context.Context, env Env, pending pendingRestore) {
	if !claimPendingReport(ctx, env, pending) {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", statusDegraded).
		Str("reason", reasonRestoreIncomplete).
		Int("sessions", pending.sessions).
		Msg("autosave resumed after a restore with saved sessions missing")
}

func reportPendingRestore(ctx context.Context, env Env, pending pendingRestore) {
	if !claimPendingReport(ctx, env, pending) {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", statusSkipped).
		Str("reason", reasonRestorePending).
		Int("sessions", pending.sessions).
		Msg("tmux started after the newest snapshot and its saved sessions are not restored")
}

func logPendingRestoreFailure(ctx context.Context, reason string) {
	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "autosave").
		Str("status", statusDegraded).
		Str("reason", reason).
		Msg("pending restore state could not be checked")
}
