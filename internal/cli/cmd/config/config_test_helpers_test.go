package cmdconfig

import (
	"context"

	"github.com/spf13/cobra"
)

const testConfigFlag = "--config"

func newRootCommand(_ context.Context) *cobra.Command {
	rootCommand := &cobra.Command{
		Use:           "sesh",
		Short:         "Native tmux session persistence",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCommand.PersistentFlags().String("config", "", "Explicit native TOML configuration file")
	rootCommand.PersistentFlags().Bool("no-config", false, "Use built-in settings without reading configuration files")

	rootCommand.AddCommand(
		NewCommand(),
	)

	return rootCommand
}
