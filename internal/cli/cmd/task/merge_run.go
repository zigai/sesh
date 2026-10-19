//go:build linux || darwin

package task

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
)

const mergeRunVerb = "merge-run"

func newMergeRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         mergeRunVerb + " <task>",
		Short:       "Merge a task in the task's popup window",
		Hidden:      true,
		Args:        clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE:        runMergeRun,
		Annotations: map[string]string{clibase.AnnotationOperation: "task_merge", clibase.AnnotationLiveIO: "true"},
	}

	actioncli.AddEnvironmentFlags(cmd)

	return cmd
}

func runMergeRun(cmd *cobra.Command, args []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	run, err := runtime.Local.RunTaskMerge(ctx, args[0], gotmux.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	err = errors.Join(err, runtime.Close())

	if run.Ran && run.ExitCode != 0 {
		_, _ = fmt.Fprintf(os.Stdout, "\nmerge stopped with exit code %d\n", run.ExitCode)
	}

	return err
}
