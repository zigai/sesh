package autosave

import (
	"context"
	"errors"
	"os"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
)

const reasonFinalSaveMissing = "final_save_missing"

func previousShutdownUnsaved(lastSavedUnix, finalSaveUnix, bootUnix int64) bool {
	return lastSavedUnix > 0 && lastSavedUnix < bootUnix && finalSaveUnix < lastSavedUnix
}

func reportUnsavedShutdownSinceBoot(ctx context.Context, env Env) {
	bootUnix, err := bootTimeUnix()
	if err != nil {
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
