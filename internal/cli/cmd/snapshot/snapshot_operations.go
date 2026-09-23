package cmdsnapshot

import (
	"errors"
	"path/filepath"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var errOperationRequired = errors.New("--operation is required")

func newSnapshotReconcileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "reconcile",
		Short:       "Inspect and confirm the durable journal of a restore operation",
		Long:        "Inspect and confirm the durable journal of a restore operation. This does not replay effects, inspect live tmux state, or release its recovery source.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runSnapshotOperation,
		Annotations: map[string]string{clibase.AnnotationOperation: "reconcile_restore"},
	}
	addSnapshotOperationFlags(cmd)

	return cmd
}

func newSnapshotAbandonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "abandon",
		Short:       "Explicitly abandon a restore and release its recovery source pin",
		Long:        "Explicitly abandon a restore and release its recovery source pin. Existing tmux objects are retained. Uncertain external effects remain uncertain; abandonment never rolls them back.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runSnapshotOperation,
		Annotations: map[string]string{clibase.AnnotationOperation: "abandon_restore", clibase.AnnotationSuccessOwned: "true"},
	}
	addSnapshotOperationFlags(cmd)

	return cmd
}

func addSnapshotOperationFlags(cmd *cobra.Command) {
	cmd.Flags().String("repository", "", "Native repository directory (required)")
	cmd.Flags().String("operation", "", "Restore operation ID (required)")
	clibase.AddJSONFlag(cmd)
}

func runSnapshotOperation(cmd *cobra.Command, _ []string) error {
	if err := validateSnapshotOperation(cmd); err != nil {
		return err
	}

	path, _ := cmd.Flags().GetString("repository")
	id, _ := cmd.Flags().GetString("operation")

	path, err := filepath.Abs(path)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_repository_path").Wrap(err)
	}

	repo, err := store.OpenExisting(cmd.Context(), path)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	var result engine.OperationStatus
	if cmd.Name() == "abandon" {
		result, err = engine.AbandonRestore(cmd.Context(), repo, id)
	} else {
		result, err = engine.ReconcileRestore(cmd.Context(), repo, id)
	}

	err = errors.Join(err, repo.Close())
	if err != nil {
		return oops.In("cli").With("operation", commandOperation(cmd)).Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, result)
	}

	return clibase.WriteOutput(cmd, "Operation: %s\nState: %s\nSettled: %t\nUncertain actions: %d\n", result.OperationID, result.State, result.Settled, len(result.Uncertain))
}

func ValidateSnapshotOperation(cmd *cobra.Command) error {
	return validateSnapshotOperation(cmd)
}

func ValidateSnapshotCommand(cmd *cobra.Command, args []string) error {
	switch cmd.Name() {
	case "rewrite":
		return nil
	case "reconcile", "abandon":
		return validateSnapshotOperation(cmd)
	case "prune", "gc":
		return validateSnapshotMaintenanceCommand(cmd)
	}

	if err := requiredRepository(cmd); err != nil {
		return err
	}

	switch cmd.Name() {
	case "save", "snapshot-save":
		if _, _, _, _, err := parseSaveInput(cmd, args); err != nil {
			return err
		}
	case "restore", "snapshot-restore":
		if _, _, _, err := parseRestoreInput(cmd, args); err != nil {
			return err
		}
	}

	return nil
}

func validateSnapshotOperation(cmd *cobra.Command) error {
	if err := requiredRepository(cmd); err != nil {
		return err
	}

	id, _ := cmd.Flags().GetString("operation")
	if id == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errOperationRequired)
	}

	return nil
}
