//go:build linux || darwin

package snapshot

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
)

func newSnapshotSaveCommand() *cobra.Command {
	return actioncli.Command(action.SnapshotSave, actioncli.Options[action.SaveSnapshot, action.SavedSnapshot]{
		Operation:    "save",
		SuccessOwned: true,
		Configure:    nil,
		Prepare:      nil,
		Frontend:     nil,
		Render:       renderSnapshotSave,
	})
}

func newSnapshotRestoreCommand() *cobra.Command {
	return actioncli.Command(action.SnapshotRestore, actioncli.Options[action.RestoreSnapshot, action.RestoredSnapshot]{
		Operation:    "restore",
		SuccessOwned: true,
		Configure: func(cmd *cobra.Command) {
			cmd.Flags().Bool("pick", false, "Interactively select an immutable generation from the snapshot timeline")
			cmd.Flags().Bool("verbose", false, "Also print the operation, per-pane outcomes and verification details")
		},
		Prepare:  prepareSnapshotRestore,
		Frontend: nil,
		Render:   renderSnapshotRestore,
	})
}

func renderSnapshotSave(cmd *cobra.Command, output action.SavedSnapshot, err error) error {
	if err != nil && output.Attempt == "" {
		return err
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, output, err)
	}

	writeErr := clibase.WriteOutput(cmd, "Attempt:    %s\nGeneration: %s\nStatus:     %s\n", output.Attempt, output.Generation, output.Status)
	if writeErr != nil {
		return errors.Join(err, writeErr)
	}

	return clibase.NewCommandResultError(err)
}

// prepareSnapshotRestore lets --pick choose the generation before the restore runs.
func prepareSnapshotRestore(ctx context.Context, cmd *cobra.Command, values action.Values) (bool, error) {
	if !snapshotRestorePickerEnabled(cmd) {
		return true, nil
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return false, err
	}

	generation, selected, err := selectSnapshotRestoreGeneration(ctx, cmd, workspace.Repository)
	if err != nil || !selected {
		return false, err
	}

	values.Set("generation", generation)

	return true, nil
}

func renderSnapshotRestore(cmd *cobra.Command, output action.RestoredSnapshot, err error) error {
	if err != nil && output.OperationID == "" && len(output.SessionsSkipped) == 0 {
		return err
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, output, err)
	}

	var out strings.Builder

	writeRestoreSummary(&out, output)

	if getBoolFlag(cmd, "verbose") {
		out.WriteString("\n")
		writeRestoreDetails(&out, output)
	}

	if writeErr := clibase.WriteOutput(cmd, "%s", out.String()); writeErr != nil {
		return errors.Join(err, writeErr)
	}

	return clibase.NewCommandResultError(err)
}
