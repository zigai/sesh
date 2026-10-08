package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/daemon"
)

const (
	defaultDaemonStopGrace    = 5 * time.Second
	defaultDaemonReadyTimeout = 10 * time.Second
)

var errDaemonSocketRequired = errors.New("--socket must be an explicit absolute path")

// NewCommand returns the group of long-running processes a service manager starts and stops,
// including those other packages contribute.
func NewCommand(processes ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-running processes started by service managers",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  clibase.RequireSubcommand,
	}
	cmd.AddCommand(newDaemonTmuxCommand(), newDaemonWaitReadyCommand())
	cmd.AddCommand(processes...)

	return cmd
}

func newDaemonTmuxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tmux",
		Short: "Supervise a socket-bound foreground tmux server",
		Long: "Supervise a socket-bound foreground tmux server prepared through published gotmux. " +
			"The process remains in the foreground and is joined before the command exits.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationLiveIO:    strconv.FormatBool(true),
			clibase.AnnotationOperation: "daemon_serve",
		},
		RunE: runDaemonServe,
	}
	cmd.Flags().String("socket", "", "Explicit absolute path to the owned tmux server socket (required)")
	cmd.Flags().String("tmux", "tmux", "Path of the tmux executable")
	cmd.Flags().String("tmux-config", "", "Configuration file for the tmux server [default: tmux's own configuration lookup]")
	cmd.Flags().Duration("stop-grace", defaultDaemonStopGrace, "Grace period before a forced process stop is reported incomplete")

	return cmd
}

func newDaemonWaitReadyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "wait-ready",
		Short:       "Wait for an explicitly selected tmux server to answer",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{clibase.AnnotationOperation: "daemon_wait_ready"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			socket, _ := cmd.Flags().GetString("socket")
			timeout, _ := cmd.Flags().GetDuration("timeout")

			if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || timeout <= 0 {
				return clibase.NewExitError(clibase.ExitCodeUsage, errDaemonSocketRequired)
			}

			ctx, cancel := context.WithTimeout(clibase.CommandContext(cmd), timeout)
			defer cancel()

			if err := daemon.WaitReady(ctx, socket); err != nil {
				return oops.In("cli").Code("daemon_not_ready").With("operation", "daemon_wait_ready").Wrap(err)
			}

			return nil
		},
	}
	cmd.Flags().String("socket", "", "Explicit absolute path to the tmux server socket (required)")
	cmd.Flags().Duration("timeout", defaultDaemonReadyTimeout, "Maximum readiness wait")

	return cmd
}

func runDaemonServe(cmd *cobra.Command, _ []string) error {
	socket, _ := cmd.Flags().GetString("socket")
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return clibase.NewExitError(clibase.ExitCodeUsage, errDaemonSocketRequired)
	}

	binary, _ := cmd.Flags().GetString("tmux")
	configFile, _ := cmd.Flags().GetString("tmux-config")
	stopGrace, _ := cmd.Flags().GetDuration("stop-grace")

	supervisor, err := daemon.NewTmuxSupervisor(binary, socket, configFile, stopGrace)
	if err != nil {
		if errors.Is(err, daemon.ErrSocketRequired) || errors.Is(err, daemon.ErrStopGrace) {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		return oops.In("cli").Code("daemon_config_failed").With("operation", "daemon_serve").Wrap(err)
	}

	if err := supervisor.Run(clibase.CommandContext(cmd)); err != nil {
		return oops.In("cli").Code("daemon_supervision_failed").With("operation", "daemon_serve").Wrap(err)
	}

	return nil
}
