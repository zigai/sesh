//go:build linux || darwin

package engine

import (
	"context"

	"github.com/zigai/sesh/internal/snapshot/store"
)

// RestorableSessionCount is the number of sessions in the newest practical-recovery generation.
// It is zero when the repository has no such generation or it cannot be read; callers that only
// advise the user treat both the same way.
func RestorableSessionCount(ctx context.Context, repository *store.Repository) int {
	generation, _, err := repository.ReadPracticalCurrent(ctx)
	if err != nil {
		logRecoveryInspectionFailure(ctx, "count_restorable", "current", err)
		return 0
	}

	manifest, err := repository.Read(ctx, generation.ID)
	if err != nil {
		logRecoveryInspectionFailure(ctx, "count_restorable", "manifest", err)
		return 0
	}

	return len(manifest.Sessions)
}
