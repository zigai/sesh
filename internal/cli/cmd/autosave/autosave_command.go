package autosave

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	autosaveStatusSaved    = "saved"
	autosaveStatusSkipped  = "skipped"
	autosaveStatusDegraded = "degraded"
	autosaveStatusFailed   = "failed"

	autosaveReasonLockBusy           = "lock_busy"
	autosaveReasonTmuxNotRunning     = "tmux_not_running"
	autosaveReasonRestorableSessions = "restorable_sessions"

	autosaveHistoryRecorded    = "recorded"
	autosaveHistoryNeverRun    = "never_run"
	autosaveHistoryUnavailable = "unavailable"

	autosaveRepositoryMissing     = "missing"
	autosaveRepositoryEmpty       = "empty"
	autosaveRepositoryObservation = "observation_incomplete"
	autosaveRepositoryRecovery    = "recovery_required"
	autosaveRepositoryReady       = "ready"

	AutosaveDaemonUnselected   = "unselected"
	autosaveDaemonUnselected   = AutosaveDaemonUnselected
	autosaveDaemonReady        = "ready"
	autosaveDaemonNotReady     = "not_ready"
	autosaveDaemonAbsent       = "absent"
	autosaveDaemonStale        = "stale"
	autosaveDaemonUnverifiable = "unverifiable"
)

var (
	errAutosaveStartupRefused      = errors.New("startup restore refused")
	errAutosaveHistoryUnavailable  = errors.New("autosave history state is unavailable")
	errAutosaveBookkeeping         = errors.New("autosave bookkeeping did not complete")
	errAutosaveIncomplete          = errors.New("autosave did not complete within its deadline")
	errAutosaveSaveSkippedLockBusy = errors.New("another autosave holds the lock")
)

type autosaveOutcome struct {
	status         string
	reason         string
	attempt        string
	generation     string
	snapshot       string
	history        string
	repository     string
	finalSave      string
	defaultSession string
	defaultFrom    string
	defaultTo      string
	recorded       bool
	restorable     int
	daemonState    string
	daemonStatus   daemon.RuntimeStatus
}

type autosaveEnv struct {
	cfg           *config.Config
	historyPath   string
	interval      time.Duration
	historyKnown  bool
	historyErr    error
	decision      automation.DueDecision
	lastSavedUnix int64
}

func newAutosaveOutcome(status, reason string) autosaveOutcome {
	return autosaveOutcome{
		status:         status,
		reason:         reason,
		attempt:        "",
		generation:     "",
		snapshot:       "",
		history:        "",
		repository:     "",
		finalSave:      "",
		defaultSession: "",
		defaultFrom:    "",
		defaultTo:      "",
		recorded:       false,
		restorable:     0,
		daemonState:    autosaveDaemonUnselected,
		daemonStatus:   emptyDaemonRuntimeStatus(),
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

	cmd.AddCommand(NewStatusCommand())

	return cmd
}

func addAutosaveFlags(flags *pflag.FlagSet) {
	flags.String("socket", "", clibase.SocketFlagUsage)
	flags.String("repository", "", clibase.RepositoryFlagUsage)
	flags.String("lease-root", "", clibase.LeaseRootFlagUsage)
	flags.String("history", "", clibase.HistoryFlagUsage)
	flags.StringSlice("session", nil, "Explicit selected session IDs")
}

func NewActionSaveIfDueCommand() *cobra.Command {
	cmd := NewSaveIfDueCommand()
	cmd.Use = "autosave-save-if-due"
	addAutosaveFlags(cmd.Flags())

	return cmd
}

func NewActionRestoreOnStartCommand() *cobra.Command {
	cmd := NewRestoreOnStartCommand()
	cmd.Use = "autosave-restore-on-start"
	addAutosaveFlags(cmd.Flags())

	return cmd
}

func NewActionFinalSaveCommand() *cobra.Command {
	cmd := NewFinalSaveCommand()
	cmd.Use = "autosave-final-save"
	addAutosaveFlags(cmd.Flags())

	return cmd
}

func resolveAutosaveEnv(cmd *cobra.Command) (autosaveEnv, error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return autosaveEnv{}, err
	}

	historyPath := resolveAutosaveHistoryPath(cmd)

	lastCompleted, historyErr := loadAutosaveHistory(historyPath)

	env := autosaveEnv{
		cfg:           cfg,
		historyPath:   historyPath,
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
