//go:build !linux

package cmddaemon

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

var errDaemonRuntimeUnsupported = errors.New("daemon lifecycle runtime is available only on Linux")

func newDaemonCoordinatorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "coordinator",
		Short: "Run lifecycle coordination",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			return clibase.NewExitError(clibase.ExitCodeUsage, errDaemonRuntimeUnsupported)
		},
	}
}
