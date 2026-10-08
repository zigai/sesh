//go:build linux

package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/integration/lifecycle"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	defaultDaemonAttemptTimeout = 30 * time.Second
	autosaveBookkeepingTimeout  = 3 * time.Second
)

var (
	errDaemonRecoveryBarrier = errors.New("repository has unresolved recovery state")
	errDaemonFinalRecorded   = daemon.ErrFinalRecorded
	errDaemonPeriodicSave    = errors.New("the coordinator does not run periodic saves")
)

func newDaemonCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Run the Linux sleep and shutdown save coordinator",
		Long: "Run the Linux sleep and shutdown save coordinator in the foreground. " +
			"The runtime holds logind delay locks for shutdown and sleep before readiness, runs one bounded save when logind " +
			"announces either, and records incomplete final saves without stopping tmux. Periodic saves belong to the autosave timer.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationLiveIO:    strconv.FormatBool(true),
			clibase.AnnotationOperation: "daemon_run",
		},
		RunE: runDaemonRuntime,
	}
	cmd.Flags().String("socket", "", "Explicit absolute path to the tmux server socket (required)")
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	cmd.Flags().StringSlice("session", nil, "Explicit selected session IDs")
	cmd.Flags().String("status-file", "", clibase.StatusFileFlagUsage)
	cmd.Flags().Duration("attempt-timeout", defaultDaemonAttemptTimeout, "Save attempt bound reported in the coordinator runtime status")
	cmd.Flags().Duration("drain-grace", automation.DefaultDrainGrace, "Grace period for stopping the coordinator scheduler")

	return cmd
}

func runDaemonRuntime(cmd *cobra.Command, _ []string) (resultErr error) {
	if socket, _ := cmd.Flags().GetString("socket"); socket == "" && os.Getenv("TMUX") == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, integration.ErrEndpoint)
	}

	env, err := cmdautosave.ResolveEnv(cmd)
	if err != nil {
		return err
	}

	attemptTimeout, _ := cmd.Flags().GetDuration("attempt-timeout")
	drainGrace, _ := cmd.Flags().GetDuration("drain-grace")

	if attemptTimeout <= 0 || drainGrace <= 0 {
		return clibase.NewExitError(clibase.ExitCodeUsage, automation.ErrSchedulerAttemptTimeout)
	}

	socket := env.Workspace().Socket
	if err := validateDaemonSocket(socket); err != nil {
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

	if err := ensureDaemonRepositoryReady(ctx, env); err != nil {
		return oops.In("cli").With("operation", "inspect_daemon_repository").Wrap(err)
	}

	reporter, err := daemon.StartRuntimeReporter(ctx, daemon.RuntimeStatusConfig{
		RuntimeDir:     os.Getenv("XDG_RUNTIME_DIR"),
		Endpoint:       socket,
		LogindDelay:    logind.Delay(),
		AttemptTimeout: attemptTimeout,
		DrainGrace:     drainGrace,
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

	newScheduler := func() (*automation.Scheduler, error) {
		return newDaemonScheduler(cmd, env, attemptTimeout, drainGrace, reporter)
	}

	// The coordinator only runs the sleep and shutdown saves logind announces; periodic saves
	// belong to the autosave timer.
	scheduler, err := newScheduler()
	if err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	reporter.Update(daemon.RuntimePhaseRunning)

	return runDaemonLifecycle(ctx, scheduler, newScheduler, logind, drainGrace, reporter)
}

func validateDaemonSocket(socket string) error {
	if _, err := integration.Open(socket); err != nil {
		return oops.In("cli").With("operation", "validate_daemon_socket").Wrap(err)
	}

	if _, err := daemon.RuntimeStatusPath(os.Getenv("XDG_RUNTIME_DIR"), socket); err != nil {
		return oops.In("cli").With("operation", "validate_daemon_runtime_status").Wrap(err)
	}

	return nil
}

func newDaemonScheduler(
	cmd *cobra.Command,
	env cmdautosave.Env,
	attemptTimeout time.Duration,
	drainGrace time.Duration,
	reporter *daemon.RuntimeReporter,
) (*automation.Scheduler, error) {
	scheduler, err := automation.NewScheduler(automation.SchedulerConfig{
		Interval:       env.Interval(),
		AttemptTimeout: attemptTimeout,
		DrainGrace:     drainGrace,
		Policy:         nil,
		Probe:          nil,
		Attempt: func(ctx context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
			if trigger != automation.TriggerShutdown {
				return automation.AttemptResult{Attempt: "", Generation: "", Status: "", Recorded: false}, errDaemonPeriodicSave
			}

			if reporter != nil {
				reporter.Update(daemon.RuntimePhaseFinalSave)
			}

			return executeDaemonFinalSave(ctx, cmd, env)
		},
		Now: time.Now,
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "configure_daemon_scheduler").Wrap(err)
	}

	return scheduler, nil
}

func ensureDaemonRepositoryReady(ctx context.Context, env cmdautosave.Env) error {
	repository, err := env.Workspace().OpenExistingRepository(ctx)
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
		return errors.Join(errDaemonRecoveryBarrier, &automation.ActiveRestoresError{Operations: state.ActiveOperations})
	}

	if state.RecoveryRequired {
		return errors.Join(errDaemonRecoveryBarrier, store.ErrRecoveryRequired)
	}

	return nil
}

func runDaemonLifecycle(
	ctx context.Context,
	scheduler *automation.Scheduler,
	newScheduler func() (*automation.Scheduler, error),
	runtime lifecycle.LogindRuntime,
	drainGrace time.Duration,
	reporter *daemon.RuntimeReporter,
) error {
	if err := daemon.RunLifecycle(ctx, daemon.CoordinatorConfig{
		DrainGrace:      drainGrace,
		Runtime:         runtime,
		Reporter:        reporter,
		Scheduler:       scheduler,
		NewScheduler:    newScheduler,
		IsFinalRecorded: func(err error) bool { return errors.Is(err, errDaemonFinalRecorded) },
	}); err != nil {
		return oops.In("cli").With("operation", "run_daemon_lifecycle").Wrap(err)
	}

	return nil
}

func executeDaemonFinalSave(ctx context.Context, cmd *cobra.Command, env cmdautosave.Env) (automation.AttemptResult, error) {
	result, err := cmdautosave.FinalSave(ctx, cmd, env)
	if result.StatusRecorded && err != nil {
		err = errors.Join(errDaemonFinalRecorded, err)
	}

	return result.Attempt, err
}
