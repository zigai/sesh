package autosave

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func NewActionAdoptStartupSessionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session-adopt-startup",
		Short: "Adopt a fresh numbered startup session as the default session",
		Long: "Adopt a fresh numbered startup session as the default session.\n\n" +
			"Only the exact session shown by an attached client is considered, and only when it was created moments ago with a single " +
			"window and pane. Unrelated sessions are never renamed or killed.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		RunE: runAdoptStartupSession,
	}

	addAutosaveFlags(cmd.Flags())
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAdoptStartupSession(cmd *cobra.Command, _ []string) error {
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	ctx := clibase.CommandContext(cmd)

	socketPath := clibase.ResolveSocket(cmd)

	server, err := integration.Open(socketPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	client, currentSession, err := currentAttachedClient(ctx, server)
	if err != nil {
		return err
	}

	receipt := resolveBootstrapReceipt(ctx, server, currentSession)

	result, err := automation.EnsureDefaultSession(ctx, server, automation.DefaultSessionRequest{
		Current: currentSession,
		Client:  client,
		Receipt: receipt,
		Now:     time.Now(),
	})
	if err != nil {
		return oops.In("cli").With("operation", "ensure_default_session").Wrap(err)
	}

	outcome := newAutosaveOutcome(autosaveStatusSaved, automation.ReasonAdmitted)
	outcome.defaultSession = result.Action
	outcome.defaultFrom = result.FromSession
	outcome.defaultTo = result.ToSession
	outcome.history = autosaveHistoryState(env)

	return renderAutosaveResult(cmd, env, outcome)
}

func resolveBootstrapReceipt(ctx context.Context, server *gotmux.Server, sessionID gotmux.SessionID) automation.BootstrapReceipt {
	if sessionID == "" {
		return emptyBootstrapReceipt()
	}

	sessionHandle, err := server.Session(ctx, sessionID)
	if err != nil {
		return emptyBootstrapReceipt()
	}

	optVal, err := sessionHandle.Options().User(ctx, automation.BootstrapOption)
	if err != nil {
		return emptyBootstrapReceipt()
	}

	token, ok := optVal.Local.Get()
	if !ok {
		token, ok = optVal.Effective.Get()
	}

	if !ok || token == "" {
		return emptyBootstrapReceipt()
	}

	receipt, err := automation.ParseBootstrapReceipt(token)
	if err != nil {
		return emptyBootstrapReceipt()
	}

	return receipt
}

func emptyBootstrapReceipt() automation.BootstrapReceipt {
	return automation.BootstrapReceipt{
		ServerIdentity: "",
		SessionID:      "",
		PaneID:         "",
		PanePID:        0,
		InitialCommand: "",
	}
}

func currentAttachedClient(ctx context.Context, server *gotmux.Server) (gotmux.Client, gotmux.SessionID, error) {
	clients, err := server.Clients(ctx)
	if err != nil {
		return gotmux.Client{}, "", oops.In("cli").With("operation", "list_clients").Wrap(err)
	}

	var (
		best  gotmux.ClientInfo
		found bool
	)

	for _, candidate := range clients {
		sessionID, ok := candidate.SessionID.Get()
		if !ok || sessionID == "" {
			continue
		}

		if !found || candidate.Activity.After(best.Activity) || (candidate.Activity.Equal(best.Activity) && candidate.Name < best.Name) {
			best, found = candidate, true
		}
	}

	if !found {
		return gotmux.Client{}, "", nil
	}

	client, err := server.Client(ctx, best.Name)
	if err != nil {
		return gotmux.Client{}, "", oops.In("cli").With("operation", "resolve_client").Wrap(err)
	}

	sessionID, _ := best.SessionID.Get()

	return client, sessionID, nil
}

func NewRestoreOnStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "restore-on-start",
		Short:       "Restore the newest committed snapshot when explicitly configured",
		Long:        "Restore the newest committed snapshot when explicitly configured.\n\nA pending, failed or incomplete newest attempt keeps its recovery barrier and refuses automatic restoration.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAutosaveRestoreOnStart,
		Annotations: map[string]string{clibase.AnnotationOperation: "restore-on-start"},
	}

	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveRestoreOnStart(cmd *cobra.Command, _ []string) error {
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	return executeStartupRestore(cmd, env)
}

func executeStartupRestore(cmd *cobra.Command, env autosaveEnv) error {
	ctx := clibase.CommandContext(cmd)

	socketPath := clibase.ResolveSocket(cmd)

	leaseRoot, err := resolveAutosaveLeaseRoot(cmd, env.cfg)
	if err != nil {
		return err
	}

	repoPath, err := resolveAutosaveRepository(cmd, env.cfg)
	if err != nil {
		return err
	}

	repo, err := store.OpenExisting(ctx, repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			missing := newAutosaveOutcome(autosaveStatusSkipped, automation.ReasonRepositoryMissing)
			missing.repository = autosaveRepositoryMissing
			missing.history = autosaveHistoryState(env)

			return renderAutosaveResult(cmd, env, missing)
		}

		return oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	state, stateErr := automation.InspectStartupState(ctx, repo)
	if stateErr != nil {
		return errors.Join(oops.In("cli").With("operation", "inspect_startup_state").Wrap(stateErr), wrapCloseError(repo.Close()))
	}

	state.Enabled = env.cfg.Autosave.RestoreLastOnStart
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

	server, err := integration.Open(socketPath)
	if err != nil {
		return errors.Join(
			oops.In("cli").With("operation", "open_tmux").Wrap(err),
			wrapCloseError(repo.Close()),
			lock.Release(),
		)
	}

	result, restoreErr := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		OperationID:            "",
		LeaseRoot:              leaseRoot,
		Generation:             admission.Generation,
		Sessions:               nil,
		LaunchProcesses:        true,
		AllowMissingWorkingDir: false,
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

	restored := newAutosaveOutcome(result.Status, result.Reason)
	restored.attempt = result.OperationID
	restored.generation = result.Generation
	restored.snapshot = result.Status
	restored.repository = autosaveRepositoryState(ctx, cmd, env)
	restored.history = autosaveHistoryState(env)

	renderErr := renderAutosaveResult(cmd, env, restored)

	return errors.Join(renderErr, wrapCloseError(closeErr), lockErr)
}

func renderDeniedStartupRestore(
	cmd *cobra.Command,
	env autosaveEnv,
	admission automation.RestoreAdmission,
	state automation.StartupState,
	repo *store.Repository,
) error {
	outcome := newAutosaveOutcome(autosaveStatusSkipped, admission.Reason)
	outcome.generation = admission.Generation
	outcome.repository = autosaveRepositoryFromState(state)
	outcome.history = autosaveHistoryState(env)

	renderErr := renderAutosaveResult(cmd, env, outcome)
	closeErr := wrapCloseError(repo.Close())

	switch admission.Reason {
	case automation.ReasonRestoreDisabled, automation.ReasonRepositoryMissing:
		return errors.Join(renderErr, closeErr)
	default:
		refusal := clibase.NewExitError(clibase.ExitCodeGeneral, oops.In("cli").Code("startup_refused").With("operation", "restore_on_start", "reason", admission.Reason).Wrap(errAutosaveStartupRefused))

		return errors.Join(renderErr, refusal, closeErr)
	}
}

func autosaveRepositoryFromState(state automation.StartupState) string {
	switch {
	case state.RecoveryRequired:
		return autosaveRepositoryRecovery
	case state.ObservationOnly:
		return autosaveRepositoryObservation
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
