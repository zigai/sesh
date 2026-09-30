package session

import (
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

// NewCommand returns the group of commands a person uses to manage live tmux sessions.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage live tmux sessions",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		newListCommand(),
		newSessionNewCommand(),
		newSessionSwitchCommand(),
		newSessionRenameCommand(),
		newSessionKillCommand(),
		newSessionDeleteCommand(),
		newScrollbackCommand(),
	)

	return cmd
}
