package snapshot

import (
	"errors"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type rewriteOutput struct {
	OperationID string `json:"operationId"`
	Source      string `json:"source"`
	Replacement string `json:"replacement"`
	State       string `json:"state"`
	Empty       bool   `json:"empty"`
}

func newSnapshotRecoverCommand() *cobra.Command {
	cmd := newSnapshotGroupCommand("recover", "Inspect and finish interrupted restores and saved-session deletions")
	cmd.AddCommand(newRecoverRestoreCommand(), newRecoverDeleteCommand())

	return cmd
}

func newRecoverRestoreCommand() *cobra.Command {
	cmd := newSnapshotGroupCommand("restore", "Inspect or abandon an interrupted restore")

	show := newRecoverOperationCommand("show", "Inspect and confirm the durable journal of a restore operation",
		"Inspect and confirm the durable journal of a restore operation. This does not replay effects, inspect live tmux state, or release its recovery source. To resume it, rerun snapshot restore with --operation and the original generation and sessions.",
		"reconcile_restore", runRecoverRestoreShow)

	abandon := newRecoverOperationCommand("abandon", "Explicitly abandon a restore and release its recovery source pin",
		"Explicitly abandon a restore and release its recovery source pin. Existing tmux objects are retained. Uncertain external effects remain uncertain; abandonment never rolls them back.",
		"abandon_restore", runRecoverRestoreAbandon)
	abandon.Annotations[clibase.AnnotationSuccessOwned] = "true"

	cmd.AddCommand(show, abandon)

	return cmd
}

func newRecoverDeleteCommand() *cobra.Command {
	cmd := newSnapshotGroupCommand("delete", "Inspect or resume an interrupted saved-session deletion")

	cmd.AddCommand(
		newRecoverOperationCommand("show", "Inspect durable saved-session deletion state without replay", "",
			"reconcile_rewrite", runRecoverDeleteShow),
		newRecoverOperationCommand("resume", "Complete only the original durable saved-session deletion intent", "",
			"resume_rewrite", runRecoverDeleteResume),
	)

	return cmd
}

func newSnapshotGroupCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}
}

func newRecoverOperationCommand(name, short, long, operation string, run func(*cobra.Command, *store.Repository, string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:         name + " <operation>",
		Short:       short,
		Long:        long,
		Args:        clibase.UsageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{clibase.AnnotationOperation: operation},
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := clibase.ResolveWorkspace(cmd)
			if err != nil {
				return err
			}

			repo, err := workspace.OpenExistingRepository(cmd.Context())
			if err != nil {
				return oops.In("cli").With("operation", "open_repository").Wrap(err)
			}

			return run(cmd, repo, args[0])
		},
	}

	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runRecoverRestoreShow(cmd *cobra.Command, repo *store.Repository, id string) error {
	result, err := engine.ReconcileRestore(cmd.Context(), repo, id)

	return writeRestoreOperationResult(cmd, result, errors.Join(err, repo.Close()))
}

func runRecoverRestoreAbandon(cmd *cobra.Command, repo *store.Repository, id string) error {
	result, err := engine.AbandonRestore(cmd.Context(), repo, id)

	return writeRestoreOperationResult(cmd, result, errors.Join(err, repo.Close()))
}

func writeRestoreOperationResult(cmd *cobra.Command, result engine.OperationStatus, err error) error {
	if err != nil {
		return oops.In("cli").With("operation", commandOperation(cmd)).Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, result)
	}

	return clibase.WriteOutput(cmd, "Operation: %s\nState: %s\nSettled: %t\nUncertain actions: %d\n", result.OperationID, result.State, result.Settled, len(result.Uncertain))
}

func runRecoverDeleteShow(cmd *cobra.Command, repo *store.Repository, id string) error {
	result, err := repo.ReconcileRewrite(cmd.Context(), id)

	return writeRewriteResult(cmd, id, result, errors.Join(err, repo.Close()))
}

func runRecoverDeleteResume(cmd *cobra.Command, repo *store.Repository, id string) error {
	result, err := repo.ResumeRewrite(cmd.Context(), id)

	return writeRewriteResult(cmd, id, result, errors.Join(err, repo.Close()))
}

func writeRewriteResult(cmd *cobra.Command, id string, result store.RewriteResult, err error) error {
	output := rewriteOutput{
		OperationID: id, Source: result.Source, Replacement: result.Replacement.ID,
		State: result.State, Empty: result.Empty,
	}

	if err != nil {
		err = oops.In("cli").With("operation", commandOperation(cmd)).Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, output, err)
	}

	writeErr := clibase.WriteOutput(cmd, "Operation: %s\nSource: %s\nReplacement: %s\nState: %s\n", id, output.Source, output.Replacement, output.State)

	return errors.Join(err, writeErr)
}

// ValidateSnapshotCommand checks snapshot inputs before configuration and logging start.
func ValidateSnapshotCommand(cmd *cobra.Command, args []string) error {
	if cmd.Annotations[clibase.AnnotationOperation] == "restore" {
		return validateSnapshotRestorePicker(cmd)
	}

	if cmd.Parent() != nil && cmd.Parent().Name() == "snapshot" && (cmd.Name() == "prune" || cmd.Name() == "gc") {
		return validateSnapshotMaintenanceCommand(cmd)
	}

	return nil
}
