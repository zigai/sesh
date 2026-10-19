//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func (s *Service) withRestorableSessions(ctx context.Context, result Result) Result {
	repo, err := store.OpenExisting(ctx, s.opts.SnapshotDir)
	if err != nil {
		return result
	}

	count := engine.RestorableSessionCount(ctx, repo)
	if closeErr := repo.Close(); closeErr != nil || count == 0 {
		return result
	}

	restore := s.seshCommand("snapshot restore", []string{flagSocket, flagRepository, flagHistory})
	result.Details = append(result.Details, fmt.Sprintf("The newest snapshot holds %s; restore them with '%s' once tmux runs", plural(count, "session"), restore))

	return result
}
