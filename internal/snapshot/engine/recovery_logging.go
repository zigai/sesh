//go:build linux || darwin

package engine

import (
	"context"
	"errors"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/snapshot/store"
)

func logRecoveryInspectionFailure(ctx context.Context, operation, phase string, err error) {
	if ctx.Err() != nil || errors.Is(err, store.ErrUnavailable) {
		return
	}

	zerolog.Ctx(ctx).Warn().Str("component", "snapshot").Str("operation", operation).
		Str("status", "degraded").Str("phase", phase).Str("reason", "recovery_inspection_failed").
		Msg("saved session recovery inspection could not be completed")
}
