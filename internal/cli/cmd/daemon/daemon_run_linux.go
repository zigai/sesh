//go:build linux

package cmddaemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	defaultDaemonAttemptTimeout = 30 * time.Second
	autosaveBookkeepingTimeout  = 3 * time.Second
)

var (
	errDaemonRecoveryBarrier       = errors.New("repository has unresolved recovery state")
	errDaemonFinalRecorded         = daemon.ErrFinalRecorded
	errAutosaveSocketRequired      = errors.New("--socket is required for autosave")
	errAutosaveIncomplete          = errors.New("autosave did not complete within its deadline")
	errAutosaveBookkeeping         = errors.New("autosave bookkeeping did not complete")
	errAutosaveSaveSkippedLockBusy = errors.New("another autosave holds the lock")
)

type autosaveEnv struct {
	cfg          *config.Config
	historyPath  string
	interval     time.Duration
	historyKnown bool
	historyErr   error
	decision     automation.DueDecision
}

type daemonAutosaveOutcome struct {
	attempt    string
	generation string
	snapshot   string
	recorded   bool
}

func newDaemonRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run periodic autosave and Linux lifecycle coordination",
		Long: "Run periodic autosave and Linux lifecycle coordination in the foreground. " +
			"The runtime holds logind delay locks for shutdown and sleep before readiness, stops periodic admission during drain, " +
			"and records incomplete final saves without stopping tmux.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationLiveIO:    strconv.FormatBool(true),
			clibase.AnnotationOperation: "daemon_run",
		},
		RunE: runDaemonRuntime,
	}
	cmd.Flags().String("socket", "", "Explicit absolute path to the tmux server socket (required)")
	cmd.Flags().String("repository", "", "Native repository directory")
	cmd.Flags().String("lease-root", "", "Explicit path to shared lease root directory")
	cmd.Flags().String("history", "", "History state JSON file path")
	cmd.Flags().StringSlice("session", nil, "Explicit selected session IDs")
	cmd.Flags().String("status-file", "", "Explicit path for the final save status artifact")
	cmd.Flags().Duration("attempt-timeout", defaultDaemonAttemptTimeout, "Bound for each periodic save attempt")
	cmd.Flags().Duration("drain-grace", automation.DefaultDrainGrace, "Grace period for an in-flight periodic save before lifecycle cancellation")

	return cmd
}

func runDaemonRuntime(cmd *cobra.Command, _ []string) (resultErr error) {
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	attemptTimeout, _ := cmd.Flags().GetDuration("attempt-timeout")
	drainGrace, _ := cmd.Flags().GetDuration("drain-grace")

	if attemptTimeout <= 0 || drainGrace <= 0 {
		return clibase.NewExitError(clibase.ExitCodeUsage, automation.ErrSchedulerAttemptTimeout)
	}

	socket := resolveAutosaveSocket(cmd)
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

	if err := ensureDaemonRepositoryReady(ctx, cmd, env); err != nil {
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
		return newDaemonScheduler(cmd, env, socket, attemptTimeout, drainGrace, reporter)
	}

	scheduler, err := newScheduler()
	if err != nil {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	if err := scheduler.Start(ctx); err != nil {
		return oops.In("cli").With("operation", "start_autosave_scheduler").Wrap(err)
	}

	reporter.Update(daemon.RuntimePhaseRunning)

	return runDaemonLifecycle(ctx, scheduler, newScheduler, logind, drainGrace, reporter)
}

func newDaemonScheduler(
	cmd *cobra.Command,
	env autosaveEnv,
	socket string,
	attemptTimeout time.Duration,
	drainGrace time.Duration,
	reporter *daemon.RuntimeReporter,
) (*automation.Scheduler, error) {
	scheduler, err := automation.NewScheduler(automation.SchedulerConfig{
		Interval:       env.interval,
		AttemptTimeout: attemptTimeout,
		DrainGrace:     drainGrace,
		Policy: func(facts automation.PolicyFacts) automation.PolicyDecision {
			return daemonAutosaveDecision(env.cfg.Autosave.Enabled, env.historyKnown, facts)
		},
		Probe: func(ctx context.Context) (int, error) {
			if readyErr := ensureDaemonRepositoryReady(ctx, cmd, env); readyErr != nil {
				return 0, readyErr
			}

			server, openErr := integration.Open(socket)
			if openErr != nil {
				return 0, oops.In("cli").With("operation", "open_tmux_endpoint").Wrap(openErr)
			}

			clients, clientsErr := server.Clients(ctx)
			if clientsErr != nil {
				return 0, oops.In("cli").With("operation", "inspect_tmux_clients").Wrap(clientsErr)
			}

			return len(clients), nil
		},
		Attempt: func(ctx context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
			if trigger == automation.TriggerShutdown {
				if reporter != nil {
					reporter.Update(daemon.RuntimePhaseFinalSave)
				}

				return executeDaemonFinalSave(cmd, env, ctx)
			}

			saveErr := executeAutosaveSave(ctx, cmd, env)

			return automation.AttemptResult{Attempt: "", Generation: "", Status: "", Recorded: saveErr == nil}, saveErr
		},
		Now: time.Now,
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "configure_daemon_scheduler").Wrap(err)
	}

	return scheduler, nil
}

func daemonAutosaveDecision(enabled, historyKnown bool, facts automation.PolicyFacts) automation.PolicyDecision {
	if !enabled {
		return automation.PolicyDecision{Admit: false, Reason: automation.ReasonDisabled}
	}

	if !historyKnown {
		return automation.PolicyDecision{Admit: false, Reason: automation.ReasonHistoryUnavailable}
	}

	if !facts.AttachedKnown {
		return automation.PolicyDecision{Admit: false, Reason: automation.ReasonAttachedUnknown}
	}

	if facts.Attached > 0 {
		return automation.PolicyDecision{Admit: false, Reason: automation.ReasonAttachedClients}
	}

	return automation.PolicyDecision{Admit: true, Reason: automation.ReasonAdmitted}
}

func ensureDaemonRepositoryReady(ctx context.Context, cmd *cobra.Command, env autosaveEnv) error {
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

	state, inspectErr := automation.InspectStartupState(ctx, repository)
	closeErr := repository.Close()

	if inspectErr != nil || closeErr != nil {
		return errors.Join(inspectErr, closeErr)
	}

	if state.PendingOperations > 0 || state.RecoveryRequired {
		return errDaemonRecoveryBarrier
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

func normalizeDaemonCancellation(err error) error {
	if daemonCancellationOnly(err) {
		return nil
	}

	return err
}

func daemonCancellationOnly(err error) bool {
	return daemon.DaemonCancellationOnly(err)
}

func executeDaemonFinalSave(cmd *cobra.Command, env autosaveEnv, ctx context.Context) (automation.AttemptResult, error) {
	started := time.Now()
	outcome, status, saveErr := executeFinalSave(cmd, env, ctx)
	status.StartedAtUnix = started.Unix()
	status.CompletedAtUnix = time.Now().Unix()
	status.DurationMS = time.Since(started).Milliseconds()

	statusErr := automation.WriteFinalSaveStatus(resolveAutosaveStatusPath(cmd), status)
	if statusErr == nil && saveErr != nil {
		saveErr = errors.Join(errDaemonFinalRecorded, saveErr)
	}

	return automation.AttemptResult{
		Attempt:    outcome.attempt,
		Generation: outcome.generation,
		Status:     outcome.snapshot,
		Recorded:   outcome.recorded,
	}, errors.Join(saveErr, statusErr)
}

//nolint:cyclop // Inlined final save execution logic retained from autosave
func executeFinalSave(
	cmd *cobra.Command,
	env autosaveEnv,
	ctx context.Context,
) (daemonAutosaveOutcome, automation.FinalSaveStatus, error) {
	var outcome daemonAutosaveOutcome

	status := newFinalSaveStatus()

	socketPath := resolveAutosaveSocket(cmd)
	if socketPath == "" {
		status.Result = automation.FinalSaveFailed
		status.Reason = "socket_required"
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, clibase.NewExitError(clibase.ExitCodeUsage, errAutosaveSocketRequired)
	}

	processEnv, agentStatePath, envErr := resolveAutosaveProcessEnv(cmd, env.cfg)
	if envErr != nil {
		status.Result = automation.FinalSaveFailed
		status.Reason = "invalid_environment_keys"
		status.Bookkeeping = false

		return outcome, status, envErr
	}

	leaseRoot, err := resolveAutosaveLeaseRoot(cmd, env.cfg)
	if err != nil {
		status.Result = automation.FinalSaveFailed
		status.Reason = "lease_root_unresolved"
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, err
	}

	repo, err := openAutosaveRepository(ctx, cmd, env.cfg)
	if err != nil {
		status.Result = automation.FinalSaveFailed
		status.Reason = "repository_unavailable"
		status.Bookkeeping = false

		return outcome, status, err
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		closeErr := wrapCloseError(repo.Close())

		if errors.Is(err, automation.ErrSaveLockBusy) {
			status.Reason = "lock_busy"
			status.Incomplete = true
			status.Bookkeeping = false

			return outcome, status, errors.Join(errAutosaveSaveSkippedLockBusy, closeErr)
		}

		status.Result = automation.FinalSaveFailed
		status.Reason = "lock_failed"
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr)
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = "server_unavailable"
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, errors.Join(
			wrapCloseError(repo.Close()),
			lock.Release(),
		)
	}

	selected, err := resolveAutosaveSelection(cmd, ctx, server)
	if err != nil {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = "selection_failed"
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, errors.Join(err, wrapCloseError(repo.Close()), lock.Release())
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            agents.NewAHTStore(agentStatePath),
		Profile:                engine.RecoveryPractical,
	})

	closeErr := repo.Close()
	lockErr := lock.Release()

	outcome.attempt, outcome.generation, outcome.snapshot = result.Attempt, result.Generation, result.Status
	status.Attempt, status.Generation, status.SnapshotStatus = result.Attempt, result.Generation, result.Status

	completion := automation.EvaluateSaveCompletion(automation.SaveCompletion{
		Attempt:    result.Attempt,
		Generation: result.Generation,
		Status:     result.Status,
		Uncertain:  errors.Is(saveErr, store.ErrUncertain),
	}, saveErr)

	if saveErr != nil {
		status.Incomplete = true
		status.Bookkeeping = false

		reason := completion.Reason
		resultState := automation.FinalSaveFailed

		if errors.Is(saveErr, context.DeadlineExceeded) || errors.Is(saveErr, context.Canceled) {
			reason, resultState = "deadline_exceeded", automation.FinalSaveIncomplete
		}

		if completion.Status == automation.CompletionUncertain {
			resultState = automation.FinalSaveUncertain
		}

		status.Result, status.Reason = resultState, reason

		return outcome, status, errors.Join(
			oops.In("cli").With("operation", "autosave_final_save").Wrap(saveErr),
			wrapCloseError(closeErr),
			lockErr,
		)
	}

	if !completion.Record {
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, errors.Join(errAutosaveIncomplete, wrapCloseError(closeErr), lockErr)
	}

	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autosaveBookkeepingTimeout)
	bookkeepingErr := recordAutosave(bookkeepingCtx, env.historyPath, time.Now().Unix())

	cancel()

	outcome.recorded = bookkeepingErr == nil
	status.Bookkeeping = outcome.recorded

	if bookkeepingErr != nil {
		status.Incomplete = true
		bookkeepingErr = clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, bookkeepingErr))
	}

	if result.Status != "complete" && result.Status != "best_effort" {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = "parity_incomplete"
		status.Incomplete = true

		return outcome, status, errors.Join(errAutosaveIncomplete, bookkeepingErr, wrapCloseError(closeErr), lockErr)
	}

	return outcome, status, errors.Join(bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

func executeAutosaveSave(ctx context.Context, cmd *cobra.Command, env autosaveEnv) error {
	socketPath := resolveAutosaveSocket(cmd)
	if socketPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errAutosaveSocketRequired)
	}

	processEnv, agentStatePath, envErr := resolveAutosaveProcessEnv(cmd, env.cfg)
	if envErr != nil {
		return envErr
	}

	leaseRoot, err := resolveAutosaveLeaseRoot(cmd, env.cfg)
	if err != nil {
		return err
	}

	repo, err := openAutosaveRepository(ctx, cmd, env.cfg)
	if err != nil {
		return err
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return errors.Join(oops.In("cli").With("operation", "open_tmux").Wrap(err), wrapCloseError(repo.Close()))
	}

	selected, err := resolveAutosaveSelection(cmd, ctx, server)
	if err != nil {
		return errors.Join(err, wrapCloseError(repo.Close()))
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		closeErr := wrapCloseError(repo.Close())
		if errors.Is(err, automation.ErrSaveLockBusy) {
			return errors.Join(errAutosaveSaveSkippedLockBusy, closeErr)
		}

		return errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr)
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            agents.NewAHTStore(agentStatePath),
		Profile:                engine.RecoveryPractical,
	})

	closeErr := repo.Close()
	lockErr := lock.Release()

	if saveErr != nil {
		return errors.Join(oops.In("cli").With("operation", "autosave_save").Wrap(saveErr), wrapCloseError(closeErr), lockErr)
	}

	decision := automation.EvaluateSaveCompletion(automation.SaveCompletion{
		Attempt:    result.Attempt,
		Generation: result.Generation,
		Status:     result.Status,
		Uncertain:  false,
	}, nil)

	var bookkeepingErr error

	if decision.Record {
		bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autosaveBookkeepingTimeout)
		bookkeepingErr = recordAutosave(bookkeepingCtx, env.historyPath, time.Now().Unix())

		cancel()
	}

	if bookkeepingErr != nil {
		bookkeepingErr = clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, bookkeepingErr))
	}

	return errors.Join(bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

func newFinalSaveStatus() automation.FinalSaveStatus {
	return automation.FinalSaveStatus{
		StartedAtUnix:   0,
		CompletedAtUnix: 0,
		DurationMS:      0,
		Result:          automation.FinalSaveSaved,
		Reason:          "",
		Attempt:         "",
		Generation:      "",
		SnapshotStatus:  "",
		Recorded:        false,
		Bookkeeping:     true,
		Incomplete:      false,
	}
}

func resolveAutosaveEnv(cmd *cobra.Command) (autosaveEnv, error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return autosaveEnv{}, err
	}

	historyPath := resolveAutosaveHistoryPath(cmd)
	lastCompleted, historyErr := loadAutosaveHistory(historyPath)

	env := autosaveEnv{
		cfg:          cfg,
		historyPath:  historyPath,
		interval:     automation.EffectiveInterval(config.AutosaveInterval(cfg)),
		historyKnown: historyErr == nil,
		historyErr:   historyErr,
		decision:     automation.DueDecision{Due: false, Reason: "", Elapsed: 0},
	}

	env.decision = automation.EvaluateDueState(automation.DueState{
		Enabled:           cfg.Autosave.Enabled,
		Interval:          env.interval,
		Now:               time.Now(),
		LastCompletedUnix: lastCompleted,
		HistoryKnown:      env.historyKnown,
	})

	return env, nil
}

func loadAutosaveHistory(path string) (int64, error) {
	state, err := session.LoadState(path)
	if err != nil {
		return 0, oops.In("cli").With("operation", "load_autosave_history").Wrap(err)
	}

	if state == nil {
		return 0, nil
	}

	return state.LastAutosaveTimestamp, nil
}

func resolveAutosaveRepository(cmd *cobra.Command, cfg *config.Config) (string, error) {
	repoPath, _ := cmd.Flags().GetString("repository")
	if repoPath != "" {
		abs, err := filepath.Abs(repoPath)
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_repository_path").Wrapf(err, "resolve repository path")
		}

		return abs, nil
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	return filepath.Join(dataDir, "snapshots"), nil
}

func resolveAutosaveHistoryPath(cmd *cobra.Command) string {
	historyPath, _ := cmd.Flags().GetString("history")
	if historyPath != "" {
		if abs, err := filepath.Abs(historyPath); err == nil {
			return abs
		}
	}

	return filepath.Join(config.GetXDGPaths().StateHome, "sesh", "history.json")
}

func resolveAutosaveStatusPath(cmd *cobra.Command) string {
	statusPath, _ := cmd.Flags().GetString("status-file")
	if statusPath != "" {
		if abs, err := filepath.Abs(statusPath); err == nil {
			return abs
		}
	}

	return filepath.Join(config.GetXDGPaths().StateHome, "sesh", automation.FinalSaveStatusName)
}

func resolveAutosaveSocket(cmd *cobra.Command) string {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath != "" {
		return socketPath
	}

	if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" {
		parts := strings.Split(tmuxEnv, ",")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}

	return ""
}

func resolveAutosaveLeaseRoot(cmd *cobra.Command, cfg *config.Config) (string, error) {
	leaseRoot, _ := cmd.Flags().GetString("lease-root")
	if leaseRoot != "" {
		abs, err := filepath.Abs(leaseRoot)
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_lease_root").Wrapf(err, "resolve lease root")
		}

		return abs, nil
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
	}

	return filepath.Join(dataDir, "leases"), nil
}

func recordAutosave(ctx context.Context, historyPath string, stamp int64) error {
	if historyPath == "" {
		return nil
	}

	_, err := session.UpdateState(ctx, historyPath, func(state *session.State) {
		state.LastAutosaveTimestamp = stamp
	})
	if err != nil {
		return oops.In("cli").With("operation", "record_autosave").Wrap(err)
	}

	return nil
}

func openAutosaveRepository(ctx context.Context, cmd *cobra.Command, cfg *config.Config) (*store.Repository, error) {
	repoPath, err := resolveAutosaveRepository(cmd, cfg)
	if err != nil {
		return nil, err
	}

	repo, err := store.Open(ctx, repoPath)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	return repo, nil
}

func wrapCloseError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", "close_repository").Wrap(err)
}

func resolveAutosaveProcessEnv(cmd *cobra.Command, cfg *config.Config) ([]string, string, error) {
	var processEnv []string
	if cfg != nil {
		processEnv = cfg.Snapshot.ProcessEnvironment
	} else {
		processEnv = config.DefaultProcessEnvironment
	}

	if err := process.ValidateEnvironmentKeys(processEnv); err != nil {
		return nil, "", oops.In("cli").With("operation", "autosave_validate_env").Wrap(err)
	}

	filteredEnv := config.FilterProcessEnvironmentKeys(processEnv)

	agentPath := resolveAgentStatePath(cmd)
	if agentPath == "" && cfg != nil && cfg.AgentStatus.StorePath != "" {
		agentPath = cfg.AgentStatus.StorePath
	}

	return filteredEnv, agentPath, nil
}

func resolveAgentStatePath(cmd *cobra.Command) string {
	statePath, _ := cmd.Flags().GetString("state")
	if statePath == "" {
		return ""
	}

	abs, err := filepath.Abs(statePath)
	if err != nil {
		return statePath
	}

	return abs
}

func resolveAutosaveSelection(cmd *cobra.Command, ctx context.Context, server *gotmux.Server) ([]gotmux.SessionID, error) {
	flags, _ := cmd.Flags().GetStringSlice("session")

	selected := make([]gotmux.SessionID, 0, len(flags))
	seen := make(map[string]bool, len(flags))

	for _, name := range flags {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}

		seen[name] = true
		selected = append(selected, gotmux.SessionID(name))
	}

	if len(selected) > 0 {
		return selected, nil
	}

	live, err := session.ListLive(ctx, server, "")
	if err != nil {
		return nil, oops.In("cli").With("operation", "list_live_sessions").Wrap(err)
	}

	for _, item := range live {
		selected = append(selected, gotmux.SessionID(item.ID))
	}

	return selected, nil
}
