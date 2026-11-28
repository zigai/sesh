//go:build linux

package autosave

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var errCoordinatorRecoveryBarrier = errors.New("repository has unresolved recovery state")

type loginRestoreWatcher struct {
	cancel context.CancelFunc
	done   chan struct{}

	//exhaustruct:optional
	mu            sync.Mutex
	activeRestore context.CancelFunc
}

func NewDaemonCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Run the Linux sleep and shutdown save coordinator",
		Long: "Run the Linux sleep and shutdown save coordinator in the foreground. " +
			"The runtime holds logind delay locks for shutdown and sleep before readiness, " +
			"runs one final save within the logind " +
			"delay when logind announces either, and records incomplete final saves without stopping tmux. " +
			"Periodic saves belong to the autosave timer.",
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
	env, err := ResolveEnv(cmd)
	if err != nil {
		return err
	}

	socket := env.workspace.Socket
	if _, err := env.workspace.OpenTmux(); err != nil {
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

	if err := ensureCoordinatorRepositoryReady(ctx, env); err != nil {
		return oops.In("cli").With("operation", "inspect_daemon_repository").Wrap(err)
	}

	if err := automation.EndAnnouncement(env.workspace.FinalSaveStatus); err != nil {
		zerolog.Ctx(ctx).Warn().Str("component", "daemon").Str("operation", "run_lifecycle").
			Str("status", "degraded").
			Str("reason", "announcement_not_ended").Func(logging.Cause(err)).
			Msg("final save status still marks an earlier shutdown or sleep")
	}

	reporter, err := daemon.StartRuntimeReporter(ctx, daemon.RuntimeStatusConfig{
		RuntimeDir:  os.Getenv("XDG_RUNTIME_DIR"),
		Endpoint:    socket,
		LogindDelay: logind.Delay(),
		LiveStatus: func(statusCtx context.Context) (daemon.RuntimeLiveStatus, error) {
			status, statusErr := logind.Status(statusCtx)
			if statusErr != nil {
				return daemon.RuntimeLiveStatus{}, oops.In("cli").
					With("operation", "query_live_logind_status").
					Wrap(statusErr)
			}

			return daemon.RuntimeLiveStatus{InhibitorsHeld: status.InhibitorsHeld}, nil
		},
	})
	if err != nil {
		return oops.In("cli").With("operation", "start_daemon_runtime_status").Wrap(err)
	}
	defer func() { resultErr = errors.Join(resultErr, reporter.Close()) }()

	reporter.Update(daemon.RuntimePhaseRunning)

	watcher := startLoginRestoreWatcher(ctx, env)
	defer watcher.stop()

	if err := daemon.RunLifecycle(ctx, daemon.CoordinatorConfig{
		Runtime:  logind,
		Reporter: reporter,
		FinalSave: func(saveCtx context.Context) (daemon.FinalSaveOutcome, error) {
			watcher.cancelActive()

			return coordinatorFinalSave(saveCtx, cmd, env)
		},
		Resumed: func(context.Context) error {
			return automation.EndAnnouncement(env.workspace.FinalSaveStatus)
		},
	}); err != nil {
		return oops.In("cli").With("operation", "run_daemon_lifecycle").Wrap(err)
	}

	return nil
}

func startLoginRestoreWatcher(ctx context.Context, env Env) *loginRestoreWatcher {
	if !env.workspace.Config.Autosave.RestoreLastOnStart {
		return nil
	}

	watchCtx, cancel := context.WithCancel(ctx)
	watcher := &loginRestoreWatcher{
		cancel:        cancel,
		done:          make(chan struct{}),
		activeRestore: nil,
	}

	go func() {
		defer close(watcher.done)

		err := lifecycle.WatchUserLogins(watchCtx, os.Getuid(), func(loginCtx context.Context) {
			watcher.runRestore(loginCtx, env)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			zerolog.Ctx(ctx).Warn().
				Str("component", "daemon").
				Str("operation", "watch_user_logins").
				Str("status", statusDegraded).
				Func(logging.Cause(err)).
				Msg("deferred login restore watcher stopped")
		}
	}()

	return watcher
}

func (w *loginRestoreWatcher) runRestore(ctx context.Context, env Env) {
	restoreCtx, cancel := context.WithCancel(ctx)

	w.mu.Lock()
	w.activeRestore = cancel
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.activeRestore = nil
		w.mu.Unlock()
		cancel()
	}()

	runCoordinatorDeferredRestore(restoreCtx, env)
}

func (w *loginRestoreWatcher) cancelActive() {
	if w == nil {
		return
	}

	w.mu.Lock()
	cancel := w.activeRestore
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (w *loginRestoreWatcher) stop() {
	if w == nil {
		return
	}

	w.cancel()
	<-w.done
}

func runCoordinatorDeferredRestore(ctx context.Context, env Env) {
	if !env.workspace.Config.Autosave.RestoreLastOnStart {
		return
	}

	server, err := env.workspace.OpenTmux()
	if err != nil {
		return
	}

	repo, err := env.workspace.OpenExistingRepository(ctx)
	if err != nil {
		return
	}

	if !hasPendingStartupRestore(ctx, server, repo) {
		_ = repo.Close()

		return
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		_ = repo.Close()

		return
	}

	defer func() {
		_ = repo.Close()
		_ = lock.Release()
	}()

	if !hasPendingStartupRestore(ctx, server, repo) {
		return
	}

	settled, err := engine.SettleDeadRestores(ctx, repo)
	if err != nil {
		logDeferredRestoreDegraded(ctx, "settle_dead_restores", err)

		return
	}

	state, err := automation.InspectStartupState(ctx, repo)
	if err != nil {
		logDeferredRestoreDegraded(ctx, "inspect_startup_state", err)

		return
	}

	state.Abandoned = len(settled)
	state.Enabled = env.workspace.Config.Autosave.RestoreLastOnStart
	state.Practical = true

	admission := automation.EvaluateStartupRestore(state)
	if !admission.Allowed {
		return
	}

	if !awaitDesktopKeyring(ctx) {
		return
	}

	launches, err := app.TaskRestoreLaunches(env.workspace.Tasks, env.workspace.Config.Tasks)
	if err != nil {
		logDeferredRestoreDegraded(ctx, "task_restore_launches", err)

		return
	}

	_, _ = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		OperationID:       "",
		LeaseRoot:         env.workspace.LeaseRoot,
		Generation:        admission.Generation,
		Sessions:          nil,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirShell,
		OnCollision:       engine.CollisionPolicyDefault,
		Profile:           engine.RecoveryPractical,
		Launches:          launches,
		LoginEnvironment:  app.LoginEnvironment(ctx),
	})
}

func hasPendingStartupRestore(
	ctx context.Context,
	server *gotmux.Server,
	repo *store.Repository,
) bool {
	targeted, err := engine.RestoreTargetedServer(ctx, repo, server)
	if err != nil || targeted {
		return false
	}

	unrestored := engine.FindUnrestored(ctx, server, repo)

	return len(unrestored.Sessions) > 0
}

func logDeferredRestoreDegraded(ctx context.Context, operation string, err error) {
	zerolog.Ctx(ctx).Warn().Str("component", "daemon").Str("operation", operation).
		Str("status", statusDegraded).Func(logging.Cause(err)).
		Msg("deferred login restore could not run")
}

func coordinatorFinalSave(
	ctx context.Context,
	cmd *cobra.Command,
	env Env,
) (daemon.FinalSaveOutcome, error) {
	_, saveErr, statusErr := runFinalSave(ctx, cmd, env, true)

	return daemon.FinalSaveOutcome{Recorded: statusErr == nil}, errors.Join(saveErr, statusErr)
}

func ensureCoordinatorRepositoryReady(ctx context.Context, env Env) error {
	repository, err := env.workspace.OpenExistingRepository(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return oops.In("cli").With("operation", "open_daemon_repository").Wrap(err)
	}

	_, settleErr := engine.SettleDeadRestores(ctx, repository)

	state, inspectErr := automation.InspectStartupState(ctx, repository)
	closeErr := repository.Close()

	if err := errors.Join(settleErr, inspectErr, closeErr); err != nil {
		return err
	}

	if state.RecoveryRequired {
		return errors.Join(errCoordinatorRecoveryBarrier, store.ErrRecoveryRequired)
	}

	return nil
}
