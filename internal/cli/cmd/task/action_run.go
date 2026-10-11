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

const (
	actionRunVerb = "action-run"
	actionRunArgs = 2
)

func newActionRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         actionRunVerb + " <task> <action>",
		Short:       "Run a task action in the task's popup window",
		Hidden:      true,
		Args:        clibase.UsageArgs(cobra.ExactArgs(actionRunArgs)),
		RunE:        runActionRun,
		Annotations: map[string]string{clibase.AnnotationOperation: "task_action", clibase.AnnotationLiveIO: "true"},
	}

	actioncli.AddEnvironmentFlags(cmd)

	return cmd
}

// runActionRun runs the action with the window's terminal and ends with a line saying how it
// went, which stays readable in the window after the run.
func runActionRun(cmd *cobra.Command, args []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	run, err := runtime.Local.RunTaskAction(ctx, args[0], args[1], gotmux.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	err = errors.Join(err, runtime.Close())

	if !run.Ran {
		return err
	}

	summary := fmt.Sprintf("\n%s finished with exit code %d", args[1], run.ExitCode)
	if run.Linked {
		summary += fmt.Sprintf("; pull request #%d is %s", run.PR.Number, run.PR.State)
	}

	_, _ = fmt.Fprintln(os.Stdout, summary)

	return err
}
