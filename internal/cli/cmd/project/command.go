//go:build linux || darwin

package project

import (
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Open projects in their own sessions",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		actioncli.Command(action.ProjectOpen, actioncli.Options[action.OpenProject, action.OpenedProject]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.ProjectClone, actioncli.Options[action.CloneProject, action.OpenedProject]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.ProjectCreate, actioncli.Options[action.CreateProject, action.OpenedProject]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		cmdpicker.NewProjectPickCommand(),
		cmdpicker.NewProjectPickerUICommand(),
	)

	return cmd
}
