package autosave

import (
	"context"
	"errors"
	"math"
	"os"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const bookkeepingTimeout = 3 * time.Second

const defaultFinalSaveDeadline = 20 * time.Second

const (
	hoursPerDay           = 24
	collectionGenerations = 10000
)

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
		ServerPID:       0,
		ServerStarted:   0,
		Announcement:    false,
	}
}

func newSaveIfDueCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "save-if-due",
		Short:       "Save a snapshot when the configured autosave interval has elapsed",
		Long:        "Save a snapshot when the configured autosave interval has elapsed.\n\nAn elapsed interval always admits a capture attempt; unchanged topology does not prove unchanged terminal or application state.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAutosaveSaveIfDue,
		Annotations: map[string]string{clibase.AnnotationOperation: "save-if-due"},
	}

	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveSaveIfDue(cmd *cobra.Command, _ []string) error {
	env, err := ResolveEnv(cmd)
	if err != nil {
		return err
	}

	if env.historyErr != nil {
		degraded := newOutcome(statusDegraded, automation.ReasonHistoryUnavailable)
		degraded.history = historyUnavailable

		return renderAutosaveDegraded(cmd, env, degraded, clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveHistoryUnavailable, env.historyErr)))
	}

	if !env.decision.Due {
		skipped := newOutcome(decisionStatus(env.decision), env.decision.Reason)
		skipped.history = historyState(env)

		return renderAutosaveResult(cmd, env, skipped)
	}

	return executeAutosaveSave(clibase.CommandContext(cmd), cmd, env)
}

func executeAutosaveSave(ctx context.Context, cmd *cobra.Command, env Env) error {
	processEnv, envErr := env.workspace.ProcessEnvironment()
	if envErr != nil {
		return envErr
	}

	repo, err := env.workspace.OpenRepository(ctx)
	if err != nil {
		return err
	}

	server, err := env.workspace.OpenTmux()
	if err != nil {
		return errors.Join(err, wrapCloseError(repo.Close()))
	}

	selected, err := resolveAutosaveSelection(ctx, cmd, server)
	if errors.Is(err, gotmux.ErrNoServer) {
		return skipAutosaveWithoutServer(ctx, cmd, env, repo)
	}

	if err != nil {
		return errors.Join(err, wrapCloseError(repo.Close()))
	}

	if pending := findPendingRestore(ctx, cmd, server, repo); pending.sessions > 0 {
		switch decidePendingRestore(ctx, env, server, repo, pending) {
		case pendingRestoreSave:
			reportRestoreIncomplete(ctx, env, pending)
		case pendingRestoreRun:
			return runLateStartupRestore(cmd, env, repo, pending)
		case pendingRestoreHold:
			return skipAutosaveWhileRestorePending(ctx, cmd, env, repo, pending)
		}
	}

	if len(selected) == 0 {
		return skipAutosaveWithoutSessions(cmd, env, repo)
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		return handleAutosaveLockError(ctx, cmd, env, repo, err)
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              env.workspace.LeaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            env.workspace.AgentSource(),
		Profile:                engine.RecoveryPractical,
	})

	return finishAutosave(ctx, cmd, env, repo, lock, result, saveErr)
}

func finishAutosave(
	ctx context.Context,
	cmd *cobra.Command,
	env Env,
	repo *store.Repository,
	lock *automation.SaveLock,
	result engine.SaveResult,
	saveErr error,
) error {
	if errors.Is(saveErr, capture.ErrChanged) {
		closeErr := repo.Close()
		lockErr := lock.Release()

		// The engine logs the race; the interval stays due, so the next run retries.
		skipped := newOutcome(statusSkipped, reasonCaptureChanged)
		skipped.attempt = result.Attempt
		skipped.history = historyState(env)

		return errors.Join(renderAutosaveResult(cmd, env, skipped), wrapCloseError(closeErr), lockErr)
	}

	if saveErr != nil {
		closeErr := repo.Close()
		lockErr := lock.Release()

		return renderAutosaveDegraded(
			cmd,
			env,
			failedSaveOutcome(env, result, saveErr),
			errors.Join(oops.In("cli").With("operation", "autosave_save").Wrap(saveErr), wrapCloseError(closeErr), lockErr),
		)
	}

	cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"

	decision := automation.EvaluateSaveCompletion(automation.SaveCompletion{
		Attempt:    result.Attempt,
		Generation: result.Generation,
		Status:     result.Status,
		Uncertain:  false,
	}, nil)

	maintenanceErr := maintainAfterAutosave(ctx, repo, env, decision)

	closeErr := repo.Close()
	lockErr := lock.Release()

	outcome := newOutcome(decision.Status, decision.Reason)
	outcome.attempt = result.Attempt
	outcome.generation = result.Generation
	outcome.snapshot = result.Status
	outcome.history = historyState(env)

	if maintenanceErr != nil {
		outcome.maintenance = maintenanceFailed
	}

	var bookkeepingErr error
	if decision.Record {
		bookkeepingErr = recordCompletedAutosave(ctx, env, &outcome)
	}

	renderErr := renderAutosaveResult(cmd, env, outcome)

	return errors.Join(renderErr, bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

func newFinalSaveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "final-save",
		Short: "Run one bounded high-priority save for shutdown ordering",
		Long: "Run one bounded high-priority save for shutdown ordering.\n\n" +
			"The save runs even when the configured interval has not elapsed, respects the provided deadline and records an " +
			"incomplete result instead of reporting success. It never stops tmux, kills sessions or removes snapshots.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAutosaveFinalSave,
		Annotations: map[string]string{clibase.AnnotationOperation: "final-save"},
	}

	cmd.Flags().Duration("deadline", defaultFinalSaveDeadline, "Bounded time budget for the final save")
	cmd.Flags().String("status-file", "", clibase.StatusFileFlagUsage)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveFinalSave(cmd *cobra.Command, _ []string) error {
	env, err := ResolveEnv(cmd)
	if err != nil {
		return err
	}

	deadline, err := cmd.Flags().GetDuration("deadline")
	if err != nil {
		return oops.In("cli").With("operation", "read_deadline").Wrap(err)
	}

	if deadline <= 0 {
		return clibase.NewExitError(clibase.ExitCodeUsage, oops.In("cli").Code("invalid_argument").With("operation", "save", "reason", "deadline_negative").Wrap(errAutosaveIncomplete))
	}

	ctx, cancel := context.WithTimeout(clibase.CommandContext(cmd), deadline)
	defer cancel()

	outcome, saveErr, statusErr := runFinalSave(ctx, cmd, env, false)
	if saveErr != nil {
		return errors.Join(
			renderAutosaveDegraded(cmd, env, outcome, errors.Join(saveErr, statusErr)),
			statusErr,
		)
	}

	cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"

	renderErr := renderAutosaveResult(cmd, env, outcome)

	if renderErr != nil || statusErr != nil {
		return errors.Join(renderErr, statusErr)
	}

	return nil
}

// runFinalSave records announced final saves so the final save that stopping tmux runs later
// during the same shutdown can skip a server the coordinator already saved.
func runFinalSave(ctx context.Context, cmd *cobra.Command, env Env, announced bool) (outcome, error, error) {
	started := time.Now()

	outcome, status, saveErr := executeFinalSave(ctx, cmd, env, announced)
	if outcome.reason == reasonAnnouncedStopSaved {
		return outcome, saveErr, nil
	}

	status.Announcement = announced
	status.StartedAtUnix = started.Unix()
	status.CompletedAtUnix = time.Now().Unix()
	status.DurationMS = time.Since(started).Milliseconds()

	statusErr := automation.WriteFinalSaveStatus(env.workspace.FinalSaveStatus, status)
	if statusErr != nil {
		statusErr = oops.In("cli").With("operation", "write_final_save_status").Wrap(statusErr)
	}

	return outcome, saveErr, statusErr
}

func executeFinalSave(
	ctx context.Context,
	cmd *cobra.Command,
	env Env,
	announced bool,
) (outcome, automation.FinalSaveStatus, error) {
	outcome := newOutcome(statusSaved, automation.ReasonShutdownAdmit)
	outcome.history = historyState(env)

	status := newFinalSaveStatus()

	processEnv, envErr := env.workspace.ProcessEnvironment()
	if envErr != nil {
		return finalSaveFailure(outcome, status, automation.FinalSaveFailed, "invalid_environment_keys", envErr)
	}

	repo, err := env.workspace.OpenRepository(ctx)
	if err != nil {
		return finalSaveFailure(outcome, status, automation.FinalSaveFailed, "repository_unavailable", err)
	}

	lock, err := acquireFinalSaveLock(ctx)
	if err != nil {
		return finalSaveLockFailure(outcome, status, repo, err)
	}

	server, identity, selected, reason, err := openFinalSaveTarget(ctx, cmd, env.workspace)
	if err != nil {
		return finalSaveFailure(
			outcome,
			status,
			automation.FinalSaveIncomplete,
			reason,
			errors.Join(err, wrapCloseError(repo.Close()), lock.Release()),
		)
	}

	status.ServerPID, status.ServerStarted = identity.PID, identity.Started.Unix()

	if skip, restorable := finalSaveSkipReason(ctx, cmd, env, server, repo, identity, selected, announced); skip != "" {
		if skip != reasonAnnouncedStopSaved {
			status.Result, status.Reason = automation.FinalSaveIncomplete, skip
			status.Incomplete, status.Bookkeeping = true, false
		}

		outcome.status, outcome.reason, outcome.restorable = statusSkipped, skip, restorable

		return outcome, status, errors.Join(wrapCloseError(repo.Close()), lock.Release())
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              env.workspace.LeaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            env.workspace.AgentSource(),
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
		handleFinalSaveError(saveErr, completion, &outcome, &status)

		return outcome, status, errors.Join(
			oops.In("cli").With("operation", "autosave_final_save").Wrap(saveErr),
			wrapCloseError(closeErr),
			lockErr,
		)
	}

	outcome.status, outcome.reason = completion.Status, completion.Reason

	if !completion.Record {
		status.Incomplete = true
		status.Bookkeeping = false

		return outcome, status, errors.Join(errAutosaveIncomplete, wrapCloseError(closeErr), lockErr)
	}

	bookkeepingErr := recordFinalSaveBookkeeping(ctx, env, &outcome, &status)
	if result.Status != "complete" && result.Status != "best_effort" {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = "parity_incomplete"
		status.Incomplete = true
		outcome.status = statusDegraded
		outcome.reason = "parity_incomplete"

		return outcome, status, errors.Join(errAutosaveIncomplete, bookkeepingErr, wrapCloseError(closeErr), lockErr)
	}

	return outcome, status, errors.Join(bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

func finalSaveSkipReason(
	ctx context.Context,
	cmd *cobra.Command,
	env Env,
	server *gotmux.Server,
	repo *store.Repository,
	identity gotmux.ServerIdentity,
	selected []gotmux.SessionID,
	announced bool,
) (string, int) {
	if !announced && announcedStopSaved(ctx, env, identity) {
		return reasonAnnouncedStopSaved, 0
	}

	if len(selected) == 0 {
		return reasonNoSessions, 0
	}

	if pending := findPendingRestore(ctx, cmd, server, repo); pending.sessions > 0 {
		reportPendingRestore(ctx, env, pending)

		return reasonRestorePending, pending.sessions
	}

	return "", 0
}

func openFinalSaveTarget(
	ctx context.Context,
	cmd *cobra.Command,
	workspace app.Workspace,
) (*gotmux.Server, gotmux.ServerIdentity, []gotmux.SessionID, string, error) {
	var identity gotmux.ServerIdentity

	server, err := workspace.OpenTmux()
	if err != nil {
		return nil, identity, nil, "server_unavailable", err
	}

	probe, err := server.Probe(ctx)
	if errors.Is(err, gotmux.ErrNoServer) {
		return nil, identity, nil, "server_gone", oops.In("cli").With("operation", "identify_server").Wrap(err)
	}

	if err != nil {
		return nil, identity, nil, "server_unavailable", oops.In("cli").With("operation", "identify_server").Wrap(err)
	}

	selected, err := resolveAutosaveSelection(ctx, cmd, server)
	if errors.Is(err, gotmux.ErrNoServer) {
		return nil, identity, nil, "server_gone", err
	}

	if err != nil {
		return nil, identity, nil, "selection_failed", err
	}

	return server, probe.Identity, selected, "", nil
}

func announcedStopSaved(ctx context.Context, env Env, identity gotmux.ServerIdentity) bool {
	status, err := automation.ReadFinalSaveStatus(env.workspace.FinalSaveStatus)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logStateUnavailable(ctx, "final_save_state", err)
		}

		return false
	}

	return status.CoversAnnouncedStop(identity.PID, identity.Started.Unix())
}

func finalSaveFailure(
	outcome outcome,
	status automation.FinalSaveStatus,
	result string,
	reason string,
	err error,
) (outcome, automation.FinalSaveStatus, error) {
	status.Result = result
	status.Reason = reason
	status.Incomplete = true
	status.Bookkeeping = false
	outcome.status = statusFailed
	outcome.reason = reason

	return outcome, status, err
}

func acquireFinalSaveLock(ctx context.Context) (*automation.SaveLock, error) {
	lock, _, err := automation.AcquireSaveLock(ctx, automation.SaveLockPath(), automation.SaveLockBlocking)
	if err != nil && ctx.Err() != nil {
		return nil, errors.Join(automation.ErrSaveLockBusy, err)
	}

	return lock, err
}

func finalSaveLockFailure(
	outcome outcome,
	status automation.FinalSaveStatus,
	repo *store.Repository,
	err error,
) (outcome, automation.FinalSaveStatus, error) {
	closeErr := wrapCloseError(repo.Close())

	if errors.Is(err, automation.ErrSaveLockBusy) {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = reasonLockBusy
		status.Incomplete = true
		status.Bookkeeping = false
		outcome.status = statusSkipped
		outcome.reason = reasonLockBusy

		return outcome, status, errors.Join(errAutosaveSaveSkippedLockBusy, closeErr)
	}

	return finalSaveFailure(
		outcome,
		status,
		automation.FinalSaveFailed,
		"lock_failed",
		errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr),
	)
}

func failedSaveOutcome(env Env, result engine.SaveResult, saveErr error) outcome {
	outcome := newOutcome(statusFailed, automation.ReasonSaveFailed)
	outcome.attempt = result.Attempt
	outcome.generation = result.Generation
	outcome.snapshot = result.Status
	outcome.history = historyState(env)

	if errors.Is(saveErr, store.ErrUncertain) {
		outcome.status = automation.CompletionUncertain
		outcome.reason = automation.ReasonPublicationUnsafe
	}

	return outcome
}

func handleFinalSaveError(
	saveErr error,
	completion automation.CompletionDecision,
	outcome *outcome,
	status *automation.FinalSaveStatus,
) {
	status.Incomplete = true
	status.Bookkeeping = false

	reason := completion.Reason
	resultState := automation.FinalSaveFailed

	if errors.Is(saveErr, context.DeadlineExceeded) || errors.Is(saveErr, context.Canceled) {
		reason, resultState = "deadline_exceeded", automation.FinalSaveIncomplete
	}

	if completion.Status == automation.CompletionUncertain {
		resultState = automation.FinalSaveUncertain
		outcome.status = automation.CompletionUncertain
	} else {
		outcome.status = statusFailed
	}

	status.Result, status.Reason = resultState, reason
	outcome.reason = reason
}

func recordFinalSaveBookkeeping(
	ctx context.Context,
	env Env,
	outcome *outcome,
	status *automation.FinalSaveStatus,
) error {
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	bookkeepingErr := recordAutosave(bookkeepingCtx, env.workspace.History, time.Now().Unix())

	cancel()

	outcome.recorded = bookkeepingErr == nil
	status.Recorded = outcome.recorded
	status.Bookkeeping = outcome.recorded

	if bookkeepingErr == nil {
		outcome.history = historyRecorded

		return nil
	}

	status.Incomplete = true

	return clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, bookkeepingErr))
}

func maintainAfterAutosave(ctx context.Context, repo *store.Repository, env Env, decision automation.CompletionDecision) error {
	if !decision.Record || !automation.MaintenanceDue(env.lastSavedUnix, time.Now()) {
		return nil
	}

	return maintainAutosaveRepository(ctx, repo, env.workspace.Config.Snapshot)
}

func maintainAutosaveRepository(ctx context.Context, repo *store.Repository, snapshot config.SnapshotConfig) error {
	policy := store.RetentionPolicy{
		MaxAge:   time.Duration(snapshot.RetentionDays) * hoursPerDay * time.Hour,
		MaxCount: snapshot.MaxCount,
		MaxBytes: snapshot.MaxBytes,
	}

	_, pruneErr := engine.Prune(ctx, repo, time.Now().UTC(), policy)
	_, collectErr := engine.GarbageCollect(ctx, repo, store.CollectionBudget{Generations: collectionGenerations, Bytes: math.MaxInt64})

	// The engine logs each failure; it degrades the outcome and never fails the save before it.
	return errors.Join(pruneErr, collectErr)
}

func skipAutosaveWithoutServer(ctx context.Context, cmd *cobra.Command, env Env, repo *store.Repository) error {
	restorable := reportRestorableSessions(ctx, env, repo)
	closeErr := wrapCloseError(repo.Close())

	idle := newOutcome(statusSkipped, reasonTmuxNotRunning)
	idle.history = historyState(env)
	idle.repository = repositoryState(ctx, env)
	idle.restorable = restorable

	return errors.Join(renderAutosaveResult(cmd, env, idle), closeErr)
}

func skipAutosaveWithoutSessions(cmd *cobra.Command, env Env, repo *store.Repository) error {
	closeErr := wrapCloseError(repo.Close())

	empty := newOutcome(statusSkipped, reasonNoSessions)
	empty.history = historyState(env)

	return errors.Join(renderAutosaveResult(cmd, env, empty), closeErr)
}

func recordCompletedAutosave(ctx context.Context, env Env, outcome *outcome) error {
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()

	if err := recordAutosave(bookkeepingCtx, env.workspace.History, time.Now().Unix()); err != nil {
		return clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, err))
	}

	outcome.recorded = true
	outcome.history = historyRecorded

	return nil
}

func runLateStartupRestore(cmd *cobra.Command, env Env, repo *store.Repository, pending pendingRestore) error {
	if err := wrapCloseError(repo.Close()); err != nil {
		return err
	}

	held, err := acquireStartupRestore(cmd, env)
	if held == nil {
		return err
	}

	// The episode is claimed under the lock, so a busy lock leaves the late restore to the next
	// interval while a refused restore is not retried.
	claimed, err := claimPendingEpisode(env, pending)
	if err != nil || !claimed {
		return held.skip(cmd, env, err)
	}

	return restoreUnderLock(cmd, env, held)
}

func skipAutosaveWhileRestorePending(ctx context.Context, cmd *cobra.Command, env Env, repo *store.Repository, pending pendingRestore) error {
	reportPendingRestore(ctx, env, pending)

	skipped := newOutcome(statusSkipped, reasonRestorePending)
	skipped.history = historyState(env)
	skipped.repository = repositoryState(ctx, env)
	skipped.restorable = pending.sessions

	closeErr := wrapCloseError(repo.Close())

	return errors.Join(renderAutosaveResult(cmd, env, skipped), closeErr)
}

func handleAutosaveLockError(ctx context.Context, cmd *cobra.Command, env Env, repo *store.Repository, err error) error {
	closeErr := wrapCloseError(repo.Close())

	if errors.Is(err, automation.ErrSaveLockBusy) {
		busy := newOutcome(statusSkipped, reasonLockBusy)
		busy.history = historyState(env)
		busy.repository = repositoryState(ctx, env)

		return errors.Join(renderAutosaveResult(cmd, env, busy), closeErr)
	}

	return errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr)
}
