package autosave

import (
	"context"
	"errors"
	"os"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/logging"
)

const (
	reasonFinalSaveMissing      = "final_save_missing"
	reasonFinalSaveUnverifiable = "final_save_unverifiable"
)

func previousShutdownUnsaved(lastSavedUnix, finalSaveUnix, bootUnix int64) bool {
	return lastSavedUnix > 0 && lastSavedUnix < bootUnix && finalSaveUnix < lastSavedUnix
}

func reportUnsavedShutdownSinceBoot(ctx context.Context, env Env) {
	bootUnix, err := bootTimeUnix()
	if err != nil {
		logFinalSaveCheckFailure(ctx, err)

		return
	}

	reportUnsavedShutdown(ctx, env.workspace.FinalSaveStatus, env.lastSavedUnix, bootUnix)
}

func reportUnsavedShutdown(ctx context.Context, statusPath string, lastSavedUnix, bootUnix int64) {
	finalSaveUnix := int64(0)

	status, err := automation.ReadFinalSaveStatus(statusPath)
	switch {
	case err == nil:
		finalSaveUnix = status.CompletedAtUnix
	case !errors.Is(err, os.ErrNotExist):
		logFinalSaveCheckFailure(ctx, err)

		return
	}

	if !previousShutdownUnsaved(lastSavedUnix, finalSaveUnix, bootUnix) {
		return
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "final-save").
		Str("status", "degraded").
		Str("reason", reasonFinalSaveMissing).
		Int64("last_save_before_boot_s", bootUnix-lastSavedUnix).
		Msg("previous shutdown left no final save")
}

func logFinalSaveCheckFailure(ctx context.Context, err error) {
	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "final-save").
		Str("status", "degraded").
		Str("reason", reasonFinalSaveUnverifiable).
		Func(logging.Cause(err)).
		Msg("previous shutdown could not be checked for a final save")
}
