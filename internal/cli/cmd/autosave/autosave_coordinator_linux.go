//go:build linux

package autosave

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/integration/lifecycle"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var errCoordinatorRecoveryBarrier = errors.New("repository has unresolved recovery state")

// NewDaemonCoordinatorCommand returns the long-running sleep and shutdown save coordinator.
func NewDaemonCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Run the Linux sleep and shutdown save coordinator",
		Long: "Run the Linux sleep and shutdown save coordinator in the foreground. " +
			"The runtime holds logind delay locks for shutdown and sleep before readiness, runs one final save within the logind " +
			"delay when logind announces either, and records incomplete final saves without stopping tmux. Periodic saves belong to the autosave timer.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationLiveIO:    strconv.FormatBool(true),
			clibase.AnnotationOperation: "daemon_run",
		},
		RunE: runCoordinator,
	}

	addAutosaveFlags(cmd.Flags())
	cmd.Flags().String("status-file", "", clibase.StatusFileFlagUsage)

	return cmd
}

func runCoordinator(cmd *cobra.Command, _ []string) (resultErr error) {
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	socket := clibase.ResolveSocket(cmd)
	if _, err := integration.Open(socket); err != nil {
		return clibase.NewExitError(
			clibase.ExitCodeUsage,
			oops.In("cli").With("operation", "validate_daemon_socket").Wrap(err),
		)
	}

	if _, err := daemon.RuntimeStatusPath(os.Getenv("XDG_RUNTIME_DIR"), socket); err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	ctx := clibase.CommandContext(cmd)

	logind, err := lifecycle.OpenLogind(ctx)
	if err != nil {
		return oops.In("cli").With("operation", "open_logind").Wrap(err)
	}

	defer func() { resultErr = errors.Join(resultErr, logind.Close()) }()

	if err := ensureCoordinatorRepositoryReady(ctx, cmd, env); err != nil {
		return oops.In("cli").With("operation", "inspect_daemon_repository").Wrap(err)
	}

	reporter, err := daemon.StartRuntimeReporter(ctx, daemon.RuntimeStatusConfig{
		RuntimeDir:  os.Getenv("XDG_RUNTIME_DIR"),
		Endpoint:    socket,
		LogindDelay: logind.Delay(),
		LiveStatus: func(statusCtx context.Context) (daemon.RuntimeLiveStatus, error) {
			status, statusErr := logind.Status(statusCtx)
			if statusErr != nil {
				return daemon.RuntimeLiveStatus{}, oops.In("cli").With("operation", "query_live_logind_status").Wrap(statusErr)
			}

			return daemon.RuntimeLiveStatus{InhibitorsHeld: status.InhibitorsHeld}, nil
		},
	})
	if err != nil {
		return oops.In("cli").With("operation", "start_daemon_runtime_status").Wrap(err)
	}
	defer func() { resultErr = errors.Join(resultErr, reporter.Close()) }()

	reporter.Update(daemon.RuntimePhaseRunning)

	if err := daemon.RunLifecycle(ctx, daemon.CoordinatorConfig{
		Runtime:  logind,
		Reporter: reporter,
		FinalSave: func(saveCtx context.Context) error {
			return coordinatorFinalSave(saveCtx, cmd, env)
		},
	}); err != nil {
		return oops.In("cli").With("operation", "run_daemon_lifecycle").Wrap(err)
	}

	return nil
}

// coordinatorFinalSave marks a failed final save as recorded when its status file was written, so
// the failure is reported through that file instead of stopping the coordinator.
func coordinatorFinalSave(ctx context.Context, cmd *cobra.Command, env autosaveEnv) error {
	_, saveErr, statusErr := runFinalSave(ctx, cmd, env)
	if saveErr != nil && statusErr == nil {
		saveErr = errors.Join(daemon.ErrFinalRecorded, saveErr)
	}

	return errors.Join(saveErr, statusErr)
}

func ensureCoordinatorRepositoryReady(ctx context.Context, cmd *cobra.Command, env autosaveEnv) error {
	repositoryPath, err := resolveAutosaveRepository(cmd, env.cfg)
	if err != nil {
		return err
	}

	repository, err := store.OpenExisting(ctx, repositoryPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return oops.In("cli").With("operation", "open_daemon_repository").Wrap(err)
	}

	// Restores aimed at a vanished server can never resume and must not block saving.
	_, settleErr := engine.SettleDeadRestores(ctx, repository)

	state, inspectErr := automation.InspectStartupState(ctx, repository)
	closeErr := repository.Close()

	if err := errors.Join(settleErr, inspectErr, closeErr); err != nil {
		return err
	}

	if state.PendingOperations > 0 {
		return errors.Join(errCoordinatorRecoveryBarrier, &automation.ActiveRestoresError{Operations: state.ActiveOperations})
	}

	if state.RecoveryRequired {
		return errors.Join(errCoordinatorRecoveryBarrier, store.ErrRecoveryRequired)
	}

	return nil
}
