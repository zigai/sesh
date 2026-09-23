package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"

	cmdagent "github.com/zigai/sesh/internal/cli/cmd/agent"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	cmdremote "github.com/zigai/sesh/internal/cli/cmd/remote"
	cmdsession "github.com/zigai/sesh/internal/cli/cmd/session"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
)

const actionCmd = "action"

func newActionCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   actionCmd,
		Short: "Run a named session, agent, snapshot, or autosave action",
		Long:  "Run a named action used by bindings, services, and the picker.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		newActionPickerOpenCommand(ctx),
		cmdsnapshot.NewActionSaveCommand(),
		cmdsnapshot.NewActionRestoreCommand(),
		cmdsession.NewActionSessionNewCommand(),
		cmdsession.NewActionSessionRenameCommand(),
		cmdsession.NewActionSessionKillCommand(),
		cmdsession.NewActionSessionDeleteCommand(),
		cmdsession.NewActionSessionSwitchCommand(),
		cmdagent.NewActionNextCommand(),
		cmdagent.NewActionBackCommand(),
		cmdagent.NewActionFocusCommand(),
		cmdagent.NewActionStatusCommand(),
		cmdautosave.NewActionStatusCommand(),
		cmdautosave.NewActionSaveIfDueCommand(),
		cmdautosave.NewActionRestoreOnStartCommand(),
	)

	return cmd
}

func newActionPickerOpenCommand(ctx context.Context) *cobra.Command {
	cmd := cmdpicker.NewPopupCommand(ctx, cmdremote.DefaultPickerRemoteFactory)
	cmd.Use = "picker-open"
	cmd.Short = "Open the configured tmux popup for a key binding"
	_ = cmd.Flags().Set("tmux", "true")
	cmd.Flags().Lookup("tmux").DefValue = "true"

	return cmd
}
