//go:build linux || darwin

package task

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
)

const popupAttachVerb = "popup-attach"

func NewCommand(remoteFactory cmdpicker.RemoteFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Start tasks in their own windows and worktrees",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		cmdpicker.NewTaskMenuCommand(remoteFactory),
		cmdpicker.NewTaskPickerUICommand(remoteFactory),
		actioncli.Command(action.TaskNew, actioncli.Options[action.NewTask, action.StartedTask]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskOffer, actioncli.Options[action.OfferTask, action.TaskOffered]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskTab, actioncli.Options[action.OpenTab, action.OpenedTab]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskPopup, actioncli.Options[action.OpenTaskPopup, action.ClosedTaskPopup]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskRelayout, actioncli.Options[action.RelayoutTask, action.RelaidOutTask]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskSleep, actioncli.Options[action.SleepTask, action.SleptTask]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskWake, actioncli.Options[action.WakeTask, action.WokenTask]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskOpen, actioncli.Options[action.OpenTask, action.OpenedTask]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskFinish, actioncli.Options[action.FinishTask, action.FinishedTask]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskRun, actioncli.Options[action.RunTaskAction, action.StartedTaskAction]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.TaskMerge, actioncli.Options[action.MergeTask, action.StartedTaskMerge]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		newPopupAttachCommand(),
		newPlaceholderCommand(),
		newActionRunCommand(),
		newMergeRunCommand(),
	)

	return cmd
}

func newPopupAttachCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         popupAttachVerb + " <task>",
		Short:       "Attach to a task's popup terminal",
		Hidden:      true,
		Args:        clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE:        runPopupAttach,
		Annotations: map[string]string{clibase.AnnotationOperation: "task_popup", clibase.AnnotationLiveIO: "true"},
	}

	actioncli.AddEnvironmentFlags(cmd)

	return cmd
}

func runPopupAttach(cmd *cobra.Command, args []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	return errors.Join(runtime.Local.AttachTaskPopup(ctx, args[0], gotmux.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}), runtime.Close())
}
