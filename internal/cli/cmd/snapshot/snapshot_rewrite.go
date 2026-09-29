package cmdsnapshot

import (
	"errors"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type snapshotRewriteOutput struct {
	OperationID string `json:"operationId"`
	Source      string `json:"source"`
	Replacement string `json:"replacement"`
	State       string `json:"state"`
	Empty       bool   `json:"empty"`
}

func newSnapshotRewriteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "rewrite", Short: "Inspect or resume a saved-session deletion receipt",
		Args: clibase.UsageArgs(cobra.NoArgs),
		RunE: clibase.RequireSubcommand,
	}

	for _, name := range []string{"reconcile", "resume"} {
		short := "Inspect durable saved-session deletion state without replay"
		if name == "resume" {
			short = "Complete only the original durable saved-session deletion intent"
		}

		child := &cobra.Command{
			Use: name, Short: short, Args: clibase.UsageArgs(cobra.NoArgs),
			RunE:        runSnapshotRewrite,
			Annotations: map[string]string{clibase.AnnotationOperation: name + "_rewrite"},
		}
		child.Flags().String("repository", "", clibase.RepositoryFlagUsage)
		child.Flags().String("operation", "", "Saved-session rewrite operation ID (required)")
		clibase.AddJSONFlag(child)
		cmd.AddCommand(child)
	}

	return cmd
}

func runSnapshotRewrite(cmd *cobra.Command, _ []string) error {
	if err := validateSnapshotOperation(cmd); err != nil {
		return err
	}

	id, _ := cmd.Flags().GetString("operation")

	path, err := clibase.ResolveRepository(cmd)
	if err != nil {
		return err
	}

	repo, err := store.OpenExisting(cmd.Context(), path)
	if err != nil {
		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	var result store.RewriteResult
	if cmd.Name() == "resume" {
		result, err = repo.ResumeRewrite(cmd.Context(), id)
	} else {
		result, err = repo.ReconcileRewrite(cmd.Context(), id)
	}

	err = errors.Join(err, repo.Close())
	output := snapshotRewriteOutput{
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
