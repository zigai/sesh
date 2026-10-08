package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"

	cmdagent "github.com/zigai/sesh/internal/cli/cmd/agent"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	cmddaemon "github.com/zigai/sesh/internal/cli/cmd/daemon"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
)

const actionCmd = "action"

func newActionCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   actionCmd,
		Short: "Run an action started by key bindings, services or other tools",
		Long: "Run an action started by key bindings, services or other tools.\n\n" +
			"Actions are short-lived commands that something else invokes, such as a tmux key binding, a systemd step or " +
			"the session picker. Commands people run directly live in their own groups.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		cmdpicker.NewPickerOpenCommand(ctx),
		cmdagent.NewActionNextCommand(),
		cmdagent.NewActionLatestCommand(),
		cmdagent.NewActionWaitingCommand(),
		cmdagent.NewActionUnreadCommand(),
		cmdagent.NewActionFailedCommand(),
		cmdagent.NewActionToggleCommand(),
		cmdagent.NewActionBackCommand(),
		cmdagent.NewActionFocusCommand(),
		cmdagent.NewActionStatusCommand(),
		cmdagent.NewActionWindowStatusClearCommand(),
		cmdsnapshot.NewActionSaveCommand(),
		cmdsnapshot.NewActionRestoreCommand(),
		cmdautosave.NewActionSaveIfDueCommand(),
		cmdautosave.NewActionRestoreOnStartCommand(),
		cmdautosave.NewActionFinalSaveCommand(),
		cmddaemon.NewActionWaitReadyCommand(),
	)

	return cmd
}
