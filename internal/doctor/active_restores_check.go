//go:build linux || darwin

package doctor

import (
	"context"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func (s *Service) checkActiveRestores(ctx context.Context) Result {
	if s.repoStatus != StatusOK {
		return skipped(CheckActiveRestores, CheckSnapshotRepository, "repository is unusable")
	}

	repo, err := store.OpenExisting(ctx, s.opts.SnapshotDir)
	if err != nil {
		return newResult(CheckActiveRestores, StatusFail, "Cannot open the repository: "+err.Error()).
			withReason(ReasonInspectionFailed).
			withCommand(s.doctorCommand())
	}

	active, err := engine.ActiveRestores(ctx, repo)
	if closeErr := repo.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return newResult(CheckActiveRestores, StatusFail, "Cannot read active restores: "+err.Error()).
			withReason(ReasonInspectionFailed).
			withCommand(s.doctorCommand())
	}

	var blocking, dead []string

	for _, op := range active {
		if op.TargetDaemonGone(ctx) {
			dead = append(dead, op.OperationID)
		} else {
			blocking = append(blocking, op.OperationID)
		}
	}

	switch {
	case len(blocking) > 0:
		return newResult(CheckActiveRestores, StatusFail,
			plural(len(blocking), "restore")+" marked in progress; this holds back the shutdown save coordinator and startup restores").
			withReason(ReasonRecoveryRequired).
			withDetails(blocking...).
			withCommand(s.abandonCommands(blocking))
	case len(dead) > 0:
		return newResult(CheckActiveRestores, StatusInfo,
			plural(len(dead), "interrupted restore")+" whose tmux server is gone; the next startup restore abandons these").
			withReason(ReasonStale).
			withDetails(dead...)
	default:
		return newResult(CheckActiveRestores, StatusOK, "No restores in progress")
	}
}

func (s *Service) abandonCommands(ids []string) string {
	commands := make([]string, 0, len(ids))
	for _, id := range ids {
		commands = append(commands, s.seshCommand("snapshot recover restore abandon", []string{flagRepository}, id))
	}

	return strings.Join(commands, " && ")
}
