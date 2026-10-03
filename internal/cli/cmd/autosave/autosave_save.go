package autosave

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const autosaveBookkeepingTimeout = 3 * time.Second

const defaultFinalSaveDeadline = 20 * time.Second

const (
	hoursPerDay                   = 24
	autosaveCollectionGenerations = 10000
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
	}
}

func NewSaveIfDueCommand() *cobra.Command {
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
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	if env.historyErr != nil {
		degraded := newAutosaveOutcome(autosaveStatusDegraded, automation.ReasonHistoryUnavailable)
		degraded.history = autosaveHistoryUnavailable

		return renderAutosaveDegraded(cmd, env, degraded, clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveHistoryUnavailable, env.historyErr)))
	}

	if !env.decision.Due {
		skipped := newAutosaveOutcome(autosaveDecisionStatus(env.decision), env.decision.Reason)
		skipped.history = autosaveHistoryState(env)

		return renderAutosaveResult(cmd, env, skipped)
	}

	return executeAutosaveSave(clibase.CommandContext(cmd), cmd, env)
}

func executeAutosaveSave(ctx context.Context, cmd *cobra.Command, env autosaveEnv) error {
	socketPath := clibase.ResolveSocket(cmd)

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

	selected, err := resolveAutosaveSelection(ctx, cmd, server)
	if errors.Is(err, gotmux.ErrNoServer) {
		return skipAutosaveWithoutServer(ctx, cmd, env, repo)
	}

	if err != nil {
		return errors.Join(err, wrapCloseError(repo.Close()))
	}

	if pending := findPendingRestore(ctx, cmd, server, repo); pending.sessions > 0 {
		return skipAutosaveWhileRestorePending(ctx, cmd, env, repo, pending)
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		return handleAutosaveLockError(ctx, cmd, env, repo, err)
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            agents.NewAHTStore(agentStatePath),
		Profile:                engine.RecoveryPractical,
	})
	if saveErr == nil {
		cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"
	}

	decision := automation.EvaluateSaveCompletion(automation.SaveCompletion{
		Attempt:    result.Attempt,
		Generation: result.Generation,
		Status:     result.Status,
		Uncertain:  false,
	}, nil)

	maintainAfterAutosave(ctx, repo, env, decision, saveErr)

	closeErr := repo.Close()
	lockErr := lock.Release()

	if saveErr != nil {
		outcome := failedSaveOutcome(env, result, saveErr)

		return renderAutosaveDegraded(
			cmd,
			env,
			outcome,
			errors.Join(oops.In("cli").With("operation", "autosave_save").Wrap(saveErr), wrapCloseError(closeErr), lockErr),
		)
	}

	outcome := newAutosaveOutcome(decision.Status, decision.Reason)
	outcome.attempt = result.Attempt
	outcome.generation = result.Generation
	outcome.snapshot = result.Status
	outcome.history = autosaveHistoryState(env)

	var bookkeepingErr error
	if decision.Record {
		bookkeepingErr = recordCompletedAutosave(ctx, env, &outcome)
	}

	renderErr := renderAutosaveResult(cmd, env, outcome)

	return errors.Join(renderErr, bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

func NewFinalSaveCommand() *cobra.Command {
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
	env, err := resolveAutosaveEnv(cmd)
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

	started := time.Now()

	outcome, status, saveErr := executeFinalSave(ctx, cmd, env)

	status.StartedAtUnix = started.Unix()
	status.CompletedAtUnix = time.Now().Unix()
	status.DurationMS = time.Since(started).Milliseconds()

	statusErr := automation.WriteFinalSaveStatus(resolveAutosaveStatusPath(cmd), status)
	if statusErr != nil {
		statusErr = oops.In("cli").With("operation", "write_final_save_status").Wrap(statusErr)
	}

	if saveErr != nil {
		return errors.Join(
			renderAutosaveDegraded(cmd, env, outcome, errors.Join(saveErr, statusErr)),
			statusErr,
		)
	}

	renderErr := renderAutosaveResult(cmd, env, outcome)

	if renderErr != nil || statusErr != nil {
		return errors.Join(renderErr, statusErr)
	}

	return nil
}

func executeFinalSave(
	ctx context.Context,
	cmd *cobra.Command,
	env autosaveEnv,
) (autosaveOutcome, automation.FinalSaveStatus, error) {
	outcome := newAutosaveOutcome(autosaveStatusSaved, automation.ReasonShutdownAdmit)
	outcome.history = autosaveHistoryState(env)

	status := newFinalSaveStatus()

	socketPath := clibase.ResolveSocket(cmd)

	processEnv, agentStatePath, envErr := resolveAutosaveProcessEnv(cmd, env.cfg)
	if envErr != nil {
		return finalSaveFailure(outcome, status, automation.FinalSaveFailed, "invalid_environment_keys", envErr)
	}

	leaseRoot, err := resolveAutosaveLeaseRoot(cmd, env.cfg)
	if err != nil {
		return finalSaveFailure(outcome, status, automation.FinalSaveFailed, "lease_root_unresolved", err)
	}

	repo, err := openAutosaveRepository(ctx, cmd, env.cfg)
	if err != nil {
		return finalSaveFailure(outcome, status, automation.FinalSaveFailed, "repository_unavailable", err)
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		return finalSaveLockFailure(outcome, status, repo, err)
	}

	server, selected, reason, err := openFinalSaveTarget(ctx, cmd, socketPath)
	if err != nil {
		return finalSaveFailure(
			outcome,
			status,
			automation.FinalSaveIncomplete,
			reason,
			errors.Join(err, wrapCloseError(repo.Close()), lock.Release()),
		)
	}

	if pending := findPendingRestore(ctx, cmd, server, repo); pending.sessions > 0 {
		// Saving the unrestored server would make it what the next start restores.
		reportPendingRestore(ctx, env, pending)

		status.Result, status.Reason = automation.FinalSaveIncomplete, autosaveReasonRestorePending
		status.Incomplete, status.Bookkeeping = true, false
		outcome.status, outcome.reason, outcome.restorable = autosaveStatusSkipped, autosaveReasonRestorePending, pending.sessions

		return outcome, status, errors.Join(wrapCloseError(repo.Close()), lock.Release())
	}

	result, saveErr := engine.SaveObservation(ctx, repo, server, selected, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            agents.NewAHTStore(agentStatePath),
		Profile:                engine.RecoveryPractical,
	})
	if saveErr == nil {
		cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"
	}

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
		outcome.status = autosaveStatusDegraded
		outcome.reason = "parity_incomplete"

		return outcome, status, errors.Join(errAutosaveIncomplete, bookkeepingErr, wrapCloseError(closeErr), lockErr)
	}

	return outcome, status, errors.Join(bookkeepingErr, wrapCloseError(closeErr), lockErr)
}

// openFinalSaveTarget opens the tmux server and selects the sessions a final save observes. On
// failure it also returns the fixed reason the final save status records.
func openFinalSaveTarget(ctx context.Context, cmd *cobra.Command, socketPath string) (*gotmux.Server, []gotmux.SessionID, string, error) {
	server, err := integration.Open(socketPath)
	if err != nil {
		return nil, nil, "server_unavailable", oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	selected, err := resolveAutosaveSelection(ctx, cmd, server)
	if errors.Is(err, gotmux.ErrNoServer) {
		// A stop that arrives after the server died has nothing left to observe.
		return nil, nil, "server_gone", err
	}

	if err != nil {
		return nil, nil, "selection_failed", err
	}

	return server, selected, "", nil
}

func finalSaveFailure(
	outcome autosaveOutcome,
	status automation.FinalSaveStatus,
	result string,
	reason string,
	err error,
) (autosaveOutcome, automation.FinalSaveStatus, error) {
	status.Result = result
	status.Reason = reason
	status.Incomplete = true
	status.Bookkeeping = false
	outcome.status = autosaveStatusFailed
	outcome.reason = reason

	return outcome, status, err
}

func finalSaveLockFailure(
	outcome autosaveOutcome,
	status automation.FinalSaveStatus,
	repo *store.Repository,
	err error,
) (autosaveOutcome, automation.FinalSaveStatus, error) {
	closeErr := wrapCloseError(repo.Close())

	if errors.Is(err, automation.ErrSaveLockBusy) {
		status.Result = automation.FinalSaveIncomplete
		status.Reason = autosaveReasonLockBusy
		status.Incomplete = true
		status.Bookkeeping = false
		outcome.status = autosaveStatusSkipped
		outcome.reason = autosaveReasonLockBusy

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

func failedSaveOutcome(env autosaveEnv, result engine.SaveResult, saveErr error) autosaveOutcome {
	outcome := newAutosaveOutcome(autosaveStatusFailed, automation.ReasonSaveFailed)
	outcome.attempt = result.Attempt
	outcome.generation = result.Generation
	outcome.snapshot = result.Status
	outcome.history = autosaveHistoryState(env)

	if errors.Is(saveErr, store.ErrUncertain) {
		outcome.status = automation.CompletionUncertain
		outcome.reason = automation.ReasonPublicationUnsafe
	}

	return outcome
}

func handleFinalSaveError(
	saveErr error,
	completion automation.CompletionDecision,
	outcome *autosaveOutcome,
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
		outcome.status = autosaveStatusFailed
	}

	status.Result, status.Reason = resultState, reason
	outcome.reason = reason
}

func recordFinalSaveBookkeeping(
	ctx context.Context,
	env autosaveEnv,
	outcome *autosaveOutcome,
	status *automation.FinalSaveStatus,
) error {
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autosaveBookkeepingTimeout)
	bookkeepingErr := recordAutosave(bookkeepingCtx, env.historyPath, time.Now().Unix())

	cancel()

	outcome.recorded = bookkeepingErr == nil
	status.Recorded = outcome.recorded
	status.Bookkeeping = outcome.recorded

	if bookkeepingErr == nil {
		outcome.history = autosaveHistoryRecorded

		return nil
	}

	status.Incomplete = true

	return clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, bookkeepingErr))
}

// maintainAfterAutosave runs repository maintenance with the first recorded save of each day.
func maintainAfterAutosave(ctx context.Context, repo *store.Repository, env autosaveEnv, decision automation.CompletionDecision, saveErr error) {
	if saveErr != nil || !decision.Record || !automation.MaintenanceDue(env.lastSavedUnix, time.Now()) {
		return
	}

	maintainAutosaveRepository(ctx, repo, env.cfg.Snapshot)
}

// maintainAutosaveRepository applies snapshot retention and reclaims retired generations. It
// never fails the save that preceded it.
func maintainAutosaveRepository(ctx context.Context, repo *store.Repository, snapshot config.SnapshotConfig) {
	policy := store.RetentionPolicy{
		MaxAge:   time.Duration(snapshot.RetentionDays) * hoursPerDay * time.Hour,
		MaxCount: snapshot.MaxCount,
		MaxBytes: snapshot.MaxBytes,
	}

	_, _ = engine.Prune(ctx, repo, time.Now().UTC(), policy)
	_, _ = engine.GarbageCollect(ctx, repo, store.CollectionBudget{Generations: autosaveCollectionGenerations, Bytes: math.MaxInt64})
}

// skipAutosaveWithoutServer reports a skipped save when no tmux server is running on the target
// socket, such as a timer firing after login before tmux starts: there is no state to observe.
func skipAutosaveWithoutServer(ctx context.Context, cmd *cobra.Command, env autosaveEnv, repo *store.Repository) error {
	restorable := reportRestorableSessions(ctx, env, repo)
	closeErr := wrapCloseError(repo.Close())

	idle := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonTmuxNotRunning)
	idle.history = autosaveHistoryState(env)
	idle.repository = autosaveRepositoryState(ctx, cmd, env)
	idle.restorable = restorable

	return errors.Join(renderAutosaveResult(cmd, env, idle), closeErr)
}

// recordCompletedAutosave durably records the time of a saved autosave, even after the save's
// own cancellation, and marks the outcome recorded when it succeeds.
func recordCompletedAutosave(ctx context.Context, env autosaveEnv, outcome *autosaveOutcome) error {
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autosaveBookkeepingTimeout)
	defer cancel()

	if err := recordAutosave(bookkeepingCtx, env.historyPath, time.Now().Unix()); err != nil {
		return clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveBookkeeping, err))
	}

	outcome.recorded = true
	outcome.history = autosaveHistoryRecorded

	return nil
}

// skipAutosaveWhileRestorePending holds back a save while the live server lacks saved sessions
// it was started without, so the scratch state never replaces them as the newest snapshot.
func skipAutosaveWhileRestorePending(ctx context.Context, cmd *cobra.Command, env autosaveEnv, repo *store.Repository, pending pendingRestore) error {
	reportPendingRestore(ctx, env, pending)

	skipped := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonRestorePending)
	skipped.history = autosaveHistoryState(env)
	skipped.repository = autosaveRepositoryState(ctx, cmd, env)
	skipped.restorable = pending.sessions

	closeErr := wrapCloseError(repo.Close())

	return errors.Join(renderAutosaveResult(cmd, env, skipped), closeErr)
}

func handleAutosaveLockError(ctx context.Context, cmd *cobra.Command, env autosaveEnv, repo *store.Repository, err error) error {
	closeErr := wrapCloseError(repo.Close())

	if errors.Is(err, automation.ErrSaveLockBusy) {
		busy := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonLockBusy)
		busy.history = autosaveHistoryState(env)
		busy.repository = autosaveRepositoryState(ctx, cmd, env)

		return errors.Join(renderAutosaveResult(cmd, env, busy), closeErr)
	}

	return errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr)
}
