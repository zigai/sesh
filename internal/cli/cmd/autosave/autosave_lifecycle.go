package autosave

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func NewRestoreOnStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "restore-on-start",
		Short:       "Restore the newest saved snapshot when explicitly configured",
		Long:        "Restore the newest saved snapshot when explicitly configured.\n\nWhen the latest save failed or never finished, the newest saved generation is restored and reported. A pending saved-session deletion or an active restore operation refuses automatic restoration.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAutosaveRestoreOnStart,
		Annotations: map[string]string{clibase.AnnotationOperation: "restore-on-start"},
	}

	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveRestoreOnStart(cmd *cobra.Command, _ []string) error {
	env, err := ResolveEnv(cmd)
	if err != nil {
		return err
	}

	return executeStartupRestore(cmd, env)
}

func executeStartupRestore(cmd *cobra.Command, env Env) error {
	ctx := clibase.CommandContext(cmd)

	reportUnsavedShutdownSinceBoot(ctx, env)

	repo, err := env.workspace.OpenExistingRepository(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			missing := newAutosaveOutcome(autosaveStatusSkipped, automation.ReasonRepositoryMissing)
			missing.repository = autosaveRepositoryMissing
			missing.history = autosaveHistoryState(env)

			return renderAutosaveResult(cmd, env, missing)
		}

		return err
	}

	// Restores aimed at a vanished server, e.g. one interrupted by a reboot, can never resume
	// and must not block this start's restore.
	settled, settleErr := engine.SettleDeadRestores(ctx, repo)
	if settleErr != nil {
		return errors.Join(oops.In("cli").With("operation", "settle_dead_restores").Wrap(settleErr), wrapCloseError(repo.Close()))
	}

	state, stateErr := automation.InspectStartupState(ctx, repo)
	if stateErr != nil {
		return errors.Join(oops.In("cli").With("operation", "inspect_startup_state").Wrap(stateErr), wrapCloseError(repo.Close()))
	}

	state.Abandoned = len(settled)

	state.Enabled = env.workspace.Config.Autosave.RestoreLastOnStart
	state.Practical = true

	admission := automation.EvaluateStartupRestore(state)
	if !admission.Allowed {
		return renderDeniedStartupRestore(cmd, env, admission, state, repo)
	}

	lock, err := automation.TryAcquireSaveLock(ctx, automation.SaveLockPath())
	if err != nil {
		closeErr := wrapCloseError(repo.Close())

		if errors.Is(err, automation.ErrSaveLockBusy) {
			busy := newAutosaveOutcome(autosaveStatusSkipped, autosaveReasonLockBusy)
			busy.generation = admission.Generation
			busy.history = autosaveHistoryState(env)

			return errors.Join(renderAutosaveResult(cmd, env, busy), closeErr)
		}

		return errors.Join(oops.In("cli").With("operation", "acquire_autosave_lock").Wrap(err), closeErr)
	}

	server, err := env.workspace.OpenTmux()
	if err != nil {
		return errors.Join(err, wrapCloseError(repo.Close()), lock.Release())
	}

	result, restoreErr := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		OperationID:            "",
		LeaseRoot:              env.workspace.LeaseRoot,
		Generation:             admission.Generation,
		Sessions:               nil,
		LaunchProcesses:        true,
		AllowMissingWorkingDir: false,
		OnCollision:            engine.CollisionPolicyDefault,
		Profile:                engine.RecoveryPractical,
	})

	closeErr := repo.Close()
	lockErr := lock.Release()

	if restoreErr != nil {
		return errors.Join(
			oops.In("cli").With("operation", "autosave_restore").Wrap(restoreErr),
			wrapCloseError(closeErr),
			lockErr,
		)
	}

	cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"

	restored := newAutosaveOutcome(result.Status, result.Reason)
	restored.attempt = result.OperationID
	restored.generation = result.Generation
	restored.snapshot = result.Status
	restored.repository = autosaveRepositoryState(ctx, env)
	restored.history = autosaveHistoryState(env)
	restored.abandoned = state.Abandoned
	restored.restore = true
	restored.restored = len(result.SessionsRestored)
	restored.skipped = len(result.SessionsSkipped)

	renderErr := renderAutosaveResult(cmd, env, restored)

	return errors.Join(renderErr, wrapCloseError(closeErr), lockErr)
}

func renderDeniedStartupRestore(
	cmd *cobra.Command,
	env Env,
	admission automation.RestoreAdmission,
	state automation.StartupState,
	repo *store.Repository,
) error {
	outcome := newAutosaveOutcome(autosaveStatusSkipped, admission.Reason)
	outcome.generation = admission.Generation
	outcome.repository = autosaveRepositoryFromState(state)
	outcome.history = autosaveHistoryState(env)
	outcome.abandoned = state.Abandoned

	renderErr := renderAutosaveResult(cmd, env, outcome)
	closeErr := wrapCloseError(repo.Close())

	switch admission.Reason {
	case automation.ReasonRestoreDisabled, automation.ReasonRepositoryMissing:
		return errors.Join(renderErr, closeErr)
	default:
		refusal := clibase.NewExitError(clibase.ExitCodeGeneral, oops.In("cli").Code("startup_refused").With("operation", "restore_on_start", "reason", admission.Reason).
			Wrap(errors.Join(errAutosaveStartupRefused, startupRefusalCause(admission.Reason, state))))

		return errors.Join(renderErr, refusal, closeErr)
	}
}

// startupRefusalCause says why a startup restore was refused and how to clear it, so the
// journal line is actionable on its own.
func startupRefusalCause(reason string, state automation.StartupState) error {
	switch reason {
	case automation.ReasonPendingOperation:
		return &automation.ActiveRestoresError{Operations: state.ActiveOperations}
	case automation.ReasonRecoveryRequired:
		return errStartupNeedsRecovery
	case automation.ReasonObservationOnly:
		return errStartupNothingSaved
	default:
		return nil
	}
}

func autosaveRepositoryFromState(state automation.StartupState) string {
	switch {
	case state.RecoveryRequired:
		return autosaveRepositoryRecovery
	case state.Generation == "":
		return autosaveRepositoryEmpty
	default:
		return autosaveRepositoryReady
	}
}

func resolveAutosaveSelection(ctx context.Context, cmd *cobra.Command, server *gotmux.Server) ([]gotmux.SessionID, error) {
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
