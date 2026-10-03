//go:build linux || darwin

package engine

import (
	"context"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// Unrestored is a live tmux server that started after the newest snapshot and lacks some of its
// saved sessions: the state after a reboot or crash before anything was restored.
type Unrestored struct {
	Sessions      []string
	ServerStarted time.Time
}

// FindUnrestored reports the saved sessions the live tmux server is missing while the newest
// snapshot predates that server. It finds nothing when the server, the snapshot or the live
// sessions cannot be read; callers only advise or hold back automatic saves with it.
func FindUnrestored(ctx context.Context, server *gotmux.Server, repository *store.Repository) Unrestored {
	var none Unrestored

	if server == nil || repository == nil {
		return none
	}

	info, err := server.Probe(ctx)
	if err != nil {
		logRecoveryInspectionFailure(ctx, "find_unrestored", "probe", err)
		return none
	}

	_, manifest, err := repository.ReadPracticalCurrent(ctx)
	if err != nil {
		logRecoveryInspectionFailure(ctx, "find_unrestored", "current", err)
		return none
	}

	live, err := server.Sessions(ctx)
	if err != nil {
		logRecoveryInspectionFailure(ctx, "find_unrestored", "sessions", err)
		return none
	}

	saved := make([]string, 0, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		saved = append(saved, string(session.Name))
	}

	running := make([]string, 0, len(live))
	for _, session := range live {
		running = append(running, session.Name)
	}

	started := info.Identity.Started

	return Unrestored{
		Sessions:      automation.UnrestoredSessions(started, time.Unix(0, manifest.Finished), saved, running),
		ServerStarted: started,
	}
}
