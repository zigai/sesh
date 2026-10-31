package autosave

import (
	"context"
	"errors"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/session"
)

const (
	statusSaved    = "saved"
	statusSkipped  = "skipped"
	statusDegraded = "degraded"
	statusFailed   = "failed"

	reasonLockBusy           = "lock_busy"
	reasonTmuxNotRunning     = "tmux_not_running"
	reasonNoSessions         = "no_sessions"
	reasonRestorableSessions = "restorable_sessions"
	reasonCaptureChanged     = "capture_changed"
	reasonAnnouncedStopSaved = "announced_stop_saved"

	maintenanceFailed = "failed"

	historyRecorded    = "recorded"
	historyNeverRun    = "never_run"
	historyUnavailable = "unavailable"

	repositoryMissing  = "missing"
	repositoryEmpty    = "empty"
	repositoryRecovery = "recovery_required"
	repositoryReady    = "ready"

	DaemonUnselected   = "unselected"
	daemonReady        = "ready"
	daemonNotReady     = "not_ready"
	daemonAbsent       = "absent"
	daemonStale        = "stale"
	daemonUnverifiable = "unverifiable"
)

var (
	errAutosaveStartupRefused      = errors.New("startup restore refused")
	errStartupNeedsRecovery        = errors.New("the snapshot repository needs recovery before it can restore; run 'sesh doctor' for the cause and fix")
	errStartupNothingSaved         = errors.New("no saved snapshot to restore yet")
	errAutosaveHistoryUnavailable  = errors.New("autosave history state is unavailable")
	errAutosaveBookkeeping         = errors.New("autosave bookkeeping did not complete")
	errAutosaveIncomplete          = errors.New("autosave did not complete within its deadline")
	errAutosaveSaveSkippedLockBusy = errors.New("another autosave holds the lock")
)

type outcome struct {
	status       string
	reason       string
	attempt      string
	generation   string
	snapshot     string
	history      string
	repository   string
	finalSave    string
	finalSaveAt  int64
	finalSaveAge time.Duration
	recorded     bool
	restorable   int
	abandoned    int
	daemonState  string
	daemonStatus daemon.RuntimeStatus
	maintenance  string
	restore      *restoreCounts
}

type restoreCounts struct {
	restored int
	skipped  int
}

type Env struct {
	workspace     app.Workspace
	interval      time.Duration
	historyKnown  bool
	historyErr    error
	decision      automation.DueDecision
	lastSavedUnix int64
}

func newOutcome(status, reason string) outcome {
	return outcome{
		status:       status,
		reason:       reason,
		attempt:      "",
		generation:   "",
		snapshot:     "",
		history:      "",
		repository:   "",
		finalSave:    "",
		finalSaveAt:  0,
		finalSaveAge: 0,
		recorded:     false,
		restorable:   0,
		abandoned:    0,
		daemonState:  DaemonUnselected,
		daemonStatus: emptyDaemonRuntimeStatus(),
		maintenance:  "",
		restore:      nil,
	}
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autosave",
		Short: "Manage periodic and lifecycle snapshot automation",
		Long:  "Manage periodic and lifecycle snapshot automation.\n\nAutosave records practical recovery observations without claiming atomic checkpoints or exact application memory recovery.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	addAutosaveFlags(cmd.PersistentFlags())

	cmd.AddCommand(newStatusCommand(), newSaveIfDueCommand(), newRestoreOnStartCommand(), newFinalSaveCommand())

	return cmd
}

func addAutosaveFlags(flags *pflag.FlagSet) {
	flags.String("socket", "", clibase.SocketFlagUsage)
	flags.String("repository", "", clibase.RepositoryFlagUsage)
	flags.String("lease-root", "", clibase.LeaseRootFlagUsage)
	flags.String("history", "", clibase.HistoryFlagUsage)
	flags.StringSlice("session", nil, "Explicit selected session IDs")
}

func ResolveEnv(cmd *cobra.Command) (Env, error) {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return Env{}, err
	}

	cfg := workspace.Config
	lastCompleted, historyErr := loadAutosaveHistory(workspace.History)

	env := Env{
		workspace:     workspace,
		interval:      automation.EffectiveInterval(config.AutosaveInterval(cfg)),
		historyKnown:  historyErr == nil,
		historyErr:    historyErr,
		decision:      automation.DueDecision{Due: false, Reason: "", Elapsed: 0},
		lastSavedUnix: lastCompleted,
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

func (e Env) Interval() time.Duration {
	return e.interval
}

func (e Env) Workspace() app.Workspace {
	return e.workspace
}

func loadAutosaveHistory(path string) (int64, error) {
	state, err := session.LoadState(path)
	if err != nil {
		return 0, oops.In("cli").With("operation", "load_autosave_history").Wrap(err)
	}

	if state == nil {
		return 0, nil
	}

	return state.LastAutosaveUnix, nil
}

func recordAutosave(ctx context.Context, historyPath string, stamp int64) error {
	if historyPath == "" {
		return nil
	}

	_, err := session.UpdateState(ctx, historyPath, func(state *session.State) {
		state.LastAutosaveUnix = stamp
	})
	if err != nil {
		return oops.In("cli").With("operation", "record_autosave").Wrap(err)
	}

	return nil
}

func wrapCloseError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", "close_repository").Wrap(err)
}
