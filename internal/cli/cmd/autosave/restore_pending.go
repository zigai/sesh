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

const (
	reasonRestorePending    = "restore_pending"
	reasonRestoreIncomplete = "restore_incomplete"
)

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

type pendingRestoreAction int

const (
	pendingRestoreHold pendingRestoreAction = iota
	pendingRestoreRun
	pendingRestoreSave
)

// decidePendingRestore chooses what an automatic save does on a server that lacks saved sessions.
// A server a restore already targeted is saved again, since a second restore would bring back
// sessions closed since and holding back would stop every later save. A server no restore reached
// gets the startup restore once, when it is configured; otherwise the save is held back.
func decidePendingRestore(ctx context.Context, env Env, server *gotmux.Server, repo *store.Repository, pending pendingRestore) pendingRestoreAction {
	targeted, err := engine.RestoreTargetedServer(ctx, repo, server)

	switch {
	case err != nil:
		return pendingRestoreHold
	case targeted:
		return pendingRestoreSave
	case env.workspace.Config.Autosave.RestoreLastOnStart && pendingEpisodeUnhandled(env, pending):
		return pendingRestoreRun
	default:
		return pendingRestoreHold
	}
}

func pendingEpisodeUnhandled(env Env, pending pendingRestore) bool {
	if env.workspace.History == "" {
		return false
	}

	unhandled, err := automation.EpisodeUnreported(automation.RestorePendingMarkerPath(env.workspace.History), pending.serverStarted.Unix())

	return err == nil && unhandled
}

// claimPendingEpisode marks this server start handled and reports whether this call was the one
// that did, so each unrestored server is warned about or late-restored once. Without a history
// path, or when the marker cannot be read or written, it claims nothing.
func claimPendingEpisode(env Env, pending pendingRestore) bool {
	if !pendingEpisodeUnhandled(env, pending) {
		return false
	}

	return automation.RecordEpisodeReported(automation.RestorePendingMarkerPath(env.workspace.History), pending.serverStarted.Unix()) == nil
}

func reportRestoreIncomplete(ctx context.Context, env Env, pending pendingRestore) {
	if !claimPendingEpisode(env, pending) {
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
	if !claimPendingEpisode(env, pending) {
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
