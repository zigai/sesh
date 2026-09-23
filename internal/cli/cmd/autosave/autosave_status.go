package cmdautosave

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type autosaveResultJSON struct {
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	Attempt          string `json:"attempt,omitempty"`
	Generation       string `json:"generation,omitempty"`
	Snapshot         string `json:"snapshotStatus,omitempty"`
	HistoryState     string `json:"historyState,omitempty"`
	RepositoryState  string `json:"repositoryState,omitempty"`
	FinalSaveState   string `json:"finalSaveState,omitempty"`
	DefaultAction    string `json:"defaultSessionAction,omitempty"`
	DefaultFrom      string `json:"defaultSessionFrom,omitempty"`
	DefaultTo        string `json:"defaultSessionTo,omitempty"`
	Recorded         bool   `json:"recorded"`
	ElapsedSec       int64  `json:"elapsedSeconds"`
	IntervalM        int    `json:"intervalMinutes"`
	DaemonState      string `json:"daemonState"`
	DaemonPhase      string `json:"daemonPhase,omitempty"`
	InhibitorsHeld   bool   `json:"inhibitorsHeld"`
	LogindDelayMS    int64  `json:"logindDelayMillis,omitempty"`
	AttemptTimeoutMS int64  `json:"attemptTimeoutMillis,omitempty"`
	DrainGraceMS     int64  `json:"drainGraceMillis,omitempty"`
	ServerManagement string `json:"serverManagement,omitempty"`
}

func emptyDaemonRuntimeStatus() daemon.RuntimeStatus {
	return daemon.RuntimeStatus{
		Protocol:             0,
		Phase:                "",
		InhibitorsHeld:       false,
		LogindDelayMillis:    0,
		AttemptTimeoutMillis: 0,
		DrainGraceMillis:     0,
		ServerManagement:     "",
		EndpointID:           "",
		PID:                  0,
		BootID:               "",
		ProcessStartTicks:    0,
	}
}

func NewStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report autosave configuration, durable state and due state",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAutosaveStatus,
	}

	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveStatus(cmd *cobra.Command, _ []string) error {
	env, err := resolveAutosaveEnv(cmd)
	if err != nil {
		return err
	}

	ctx := clibase.CommandContext(cmd)

	outcome := newAutosaveOutcome(autosaveDecisionStatus(env.decision), env.decision.Reason)
	outcome.history = autosaveHistoryState(env)
	outcome.repository = autosaveRepositoryState(ctx, cmd, env)
	outcome.finalSave = autosaveFinalSaveState(cmd, env)
	outcome.daemonState, outcome.daemonStatus = autosaveDaemonRuntimeState(ctx, cmd)

	if env.historyErr != nil {
		outcome.status = autosaveStatusDegraded
		outcome.reason = automation.ReasonHistoryUnavailable

		return renderAutosaveDegraded(
			cmd,
			env,
			outcome,
			clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveHistoryUnavailable, env.historyErr)),
		)
	}

	return renderAutosaveResult(cmd, env, outcome)
}

func AutosaveDaemonRuntimeState(ctx context.Context, cmd *cobra.Command) (string, daemon.RuntimeStatus) {
	return autosaveDaemonRuntimeState(ctx, cmd)
}

func autosaveDaemonRuntimeState(ctx context.Context, cmd *cobra.Command) (string, daemon.RuntimeStatus) {
	endpoint := resolveAutosaveSocket(cmd)
	if endpoint == "" {
		return autosaveDaemonUnselected, emptyDaemonRuntimeStatus()
	}

	path, err := daemon.RuntimeStatusPath(os.Getenv("XDG_RUNTIME_DIR"), endpoint)
	if err != nil {
		return autosaveDaemonUnverifiable, emptyDaemonRuntimeStatus()
	}

	status, err := daemon.QueryRuntimeStatus(ctx, path)
	if err == nil {
		if status.Phase != daemon.RuntimePhaseRunning || !status.InhibitorsHeld {
			return autosaveDaemonNotReady, status
		}

		return autosaveDaemonReady, status
	}

	if errors.Is(err, os.ErrNotExist) {
		return autosaveDaemonAbsent, emptyDaemonRuntimeStatus()
	}

	if errors.Is(err, unix.ECONNREFUSED) {
		return autosaveDaemonStale, emptyDaemonRuntimeStatus()
	}

	return autosaveDaemonUnverifiable, emptyDaemonRuntimeStatus()
}

func autosaveHistoryState(env autosaveEnv) string {
	if !env.historyKnown {
		return autosaveHistoryUnavailable
	}

	if env.decision.Reason == automation.ReasonNeverRun {
		return autosaveHistoryNeverRun
	}

	return autosaveHistoryRecorded
}

func autosaveRepositoryState(ctx context.Context, cmd *cobra.Command, env autosaveEnv) string {
	repoPath, err := resolveAutosaveRepository(cmd, env.cfg)
	if err != nil {
		return ""
	}

	repository, err := store.OpenExisting(ctx, repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return autosaveRepositoryMissing
		}

		return ""
	}

	state, stateErr := automation.InspectStartupState(ctx, repository)

	closeErr := repository.Close()

	if stateErr != nil || closeErr != nil {
		return ""
	}

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

func autosaveFinalSaveState(cmd *cobra.Command, _ autosaveEnv) string {
	status, err := automation.ReadFinalSaveStatus(resolveAutosaveStatusPath(cmd))
	if err != nil {
		return ""
	}

	if status.Incomplete {
		return automation.FinalSaveIncomplete
	}

	return status.Result
}

func autosaveDecisionStatus(decision automation.DueDecision) string {
	if decision.Due {
		return "due"
	}

	if decision.Reason == automation.ReasonDisabled {
		return "disabled"
	}

	if decision.Reason == automation.ReasonHistoryUnavailable {
		return autosaveStatusDegraded
	}

	return "not_due"
}

func renderAutosaveResult(cmd *cobra.Command, env autosaveEnv, outcome autosaveOutcome) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, autosavePayload(env, outcome))
	}

	return clibase.WriteOutput(
		cmd,
		"Autosave:   %s (reason: %s)\nInterval:   %dm\nElapsed:    %s\nHistory:    %s\nRepository: %s\nAttempt:    %s\nGeneration: %s\nSnapshot:   %s\nRecorded:   %t\nDaemon:     %s\nPhase:      %s\nInhibitors: %t\n",
		outcome.status,
		outcome.reason,
		env.interval,
		env.decision.Elapsed.Round(time.Second),
		outcome.history,
		outcome.repository,
		outcome.attempt,
		outcome.generation,
		outcome.snapshot,
		outcome.recorded,
		outcome.daemonState,
		outcome.daemonStatus.Phase,
		outcome.daemonStatus.InhibitorsHeld,
	)
}

func autosavePayload(env autosaveEnv, outcome autosaveOutcome) autosaveResultJSON {
	return autosaveResultJSON{
		Status:           outcome.status,
		Reason:           outcome.reason,
		Attempt:          outcome.attempt,
		Generation:       outcome.generation,
		Snapshot:         outcome.snapshot,
		HistoryState:     outcome.history,
		RepositoryState:  outcome.repository,
		FinalSaveState:   outcome.finalSave,
		DefaultAction:    outcome.defaultSession,
		DefaultFrom:      outcome.defaultFrom,
		DefaultTo:        outcome.defaultTo,
		Recorded:         outcome.recorded,
		ElapsedSec:       int64(env.decision.Elapsed.Seconds()),
		IntervalM:        int(env.interval.Minutes()),
		DaemonState:      outcome.daemonState,
		DaemonPhase:      string(outcome.daemonStatus.Phase),
		InhibitorsHeld:   outcome.daemonStatus.InhibitorsHeld,
		LogindDelayMS:    outcome.daemonStatus.LogindDelayMillis,
		AttemptTimeoutMS: outcome.daemonStatus.AttemptTimeoutMillis,
		DrainGraceMS:     outcome.daemonStatus.DrainGraceMillis,
		ServerManagement: outcome.daemonStatus.ServerManagement,
	}
}

func renderAutosaveDegraded(cmd *cobra.Command, env autosaveEnv, outcome autosaveOutcome, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, autosavePayload(env, outcome), err)
	}

	if renderErr := renderAutosaveResult(cmd, env, outcome); renderErr != nil {
		return errors.Join(renderErr, err)
	}

	return err
}
