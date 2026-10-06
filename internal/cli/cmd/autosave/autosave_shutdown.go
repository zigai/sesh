package autosave

import (
	"context"
	"errors"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/automation"
)

const autosaveReasonFinalSaveMissing = "final_save_missing"

func previousShutdownUnsaved(lastSavedUnix, finalSaveUnix, bootUnix int64) bool {
	return lastSavedUnix > 0 && lastSavedUnix < bootUnix && finalSaveUnix < lastSavedUnix
}

func reportUnsavedShutdownSinceBoot(ctx context.Context, cmd *cobra.Command, env autosaveEnv) {
	bootUnix, err := bootTimeUnix()
	if err != nil {
		return
	}

	reportUnsavedShutdown(ctx, resolveAutosaveStatusPath(cmd), env.lastSavedUnix, bootUnix)
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
		Str("reason", autosaveReasonFinalSaveMissing).
		Int64("last_save_before_boot_s", bootUnix-lastSavedUnix).
		Msg("previous shutdown left no final save")
}
