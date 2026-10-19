//go:build !linux

package autosave

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

var errCoordinatorUnsupported = errors.New("daemon lifecycle runtime is available only on Linux")

func NewDaemonCoordinatorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "coordinator",
		Short: "Run lifecycle coordination",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			return clibase.NewExitError(clibase.ExitCodeUsage, errCoordinatorUnsupported)
		},
	}
}
