package autosave

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/daemon"
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
	FinalSaveAt      int64  `json:"finalSaveCompletedAt,omitempty"`
	Recorded         bool   `json:"recorded"`
	Restorable       int    `json:"restorableSessions,omitempty"`
	Abandoned        int    `json:"abandonedRestores,omitempty"`
	ElapsedSec       int64  `json:"elapsedSeconds"`
	IntervalM        int    `json:"intervalMinutes"`
	DaemonState      string `json:"daemonState"`
	DaemonPhase      string `json:"daemonPhase,omitempty"`
	InhibitorsHeld   bool   `json:"inhibitorsHeld"`
	LogindDelayMS    int64  `json:"logindDelayMillis,omitempty"`
	ServerManagement string `json:"serverManagement,omitempty"`
}

func emptyDaemonRuntimeStatus() daemon.RuntimeStatus {
	return daemon.RuntimeStatus{
		Protocol:          0,
		Phase:             "",
		InhibitorsHeld:    false,
		LogindDelayMillis: 0,
		ServerManagement:  "",
		EndpointID:        "",
		PID:               0,
		BootID:            "",
		ProcessStartTicks: 0,
	}
}

func NewStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "status",
		Short:       "Report autosave configuration, durable state and due state",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAutosaveStatus,
		Annotations: map[string]string{clibase.AnnotationOperation: "status"},
	}

	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAutosaveStatus(cmd *cobra.Command, _ []string) error {
	env, err := ResolveEnv(cmd)
	if err != nil {
		return err
	}

	ctx := clibase.CommandContext(cmd)

	outcome := newAutosaveOutcome(autosaveDecisionStatus(env.decision), env.decision.Reason)
	outcome.history = autosaveHistoryState(env)
	outcome.repository = autosaveRepositoryState(ctx, env)
	outcome.daemonState, outcome.daemonStatus = DaemonRuntimeState(ctx, env.workspace.Socket)

	outcome.finalSave, outcome.finalSaveAt = autosaveFinalSaveState(env)
	if outcome.finalSaveAt > 0 {
		outcome.finalSaveAge = time.Since(time.Unix(outcome.finalSaveAt, 0))
	}

	if env.historyErr != nil {
		outcome.status = autosaveStatusDegraded
		outcome.reason = automation.ReasonHistoryUnavailable

		err := clibase.NewExitError(clibase.ExitCodeGeneral, errors.Join(errAutosaveHistoryUnavailable, env.historyErr))
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, autosavePayload(env, outcome), err)
		}

		return errors.Join(clibase.WriteOutput(cmd, "%s", autosaveStatusSummary(env, outcome)), err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, autosavePayload(env, outcome))
	}

	return clibase.WriteOutput(cmd, "%s", autosaveStatusSummary(env, outcome))
}

func DaemonRuntimeState(ctx context.Context, socket string) (string, daemon.RuntimeStatus) {
	path, err := daemon.RuntimeStatusPath(os.Getenv("XDG_RUNTIME_DIR"), socket)
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

func autosaveHistoryState(env Env) string {
	if !env.historyKnown {
		return autosaveHistoryUnavailable
	}

	if env.decision.Reason == automation.ReasonNeverRun {
		return autosaveHistoryNeverRun
	}

	return autosaveHistoryRecorded
}

func autosaveRepositoryState(ctx context.Context, env Env) string {
	repository, err := env.workspace.OpenExistingRepository(ctx)
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
	case state.Generation == "":
		return autosaveRepositoryEmpty
	default:
		return autosaveRepositoryReady
	}
}

func autosaveFinalSaveState(env Env) (string, int64) {
	status, err := automation.ReadFinalSaveStatus(env.workspace.FinalSaveStatus)
	if err != nil {
		return "", 0
	}

	if status.Incomplete {
		return automation.FinalSaveIncomplete, status.CompletedAtUnix
	}

	return status.Result, status.CompletedAtUnix
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

func renderAutosaveResult(cmd *cobra.Command, env Env, outcome autosaveOutcome) error {
	logAutosaveSkip(cmd, outcome)

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, autosavePayload(env, outcome))
	}

	return clibase.WriteOutput(cmd, "%s", autosaveSummary(env, outcome))
}

func logAutosaveSkip(cmd *cobra.Command, outcome autosaveOutcome) {
	if outcome.status != autosaveStatusSkipped && outcome.reason != automation.ReasonDisabled && outcome.reason != automation.ReasonNotDue {
		return
	}

	switch outcome.reason {
	case automation.ReasonDisabled, automation.ReasonNotDue, automation.ReasonRepositoryMissing,
		autosaveReasonLockBusy, autosaveReasonTmuxNotRunning, autosaveReasonRestorePending:
	default:
		return
	}

	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}

	cmd.Annotations[clibase.AnnotationSuccessOwned] = "true"

	if outcome.reason == autosaveReasonRestorePending {
		return
	}

	zerolog.Ctx(clibase.CommandContext(cmd)).Info().Str("component", "snapshot").
		Str("operation", cmd.Annotations[clibase.AnnotationOperation]).Str("status", autosaveStatusSkipped).
		Str("reason", outcome.reason).Msg("automatic snapshot operation skipped")
}

func autosavePayload(env Env, outcome autosaveOutcome) autosaveResultJSON {
	return autosaveResultJSON{
		Status:           outcome.status,
		Reason:           outcome.reason,
		Attempt:          outcome.attempt,
		Generation:       outcome.generation,
		Snapshot:         outcome.snapshot,
		HistoryState:     outcome.history,
		RepositoryState:  outcome.repository,
		FinalSaveState:   outcome.finalSave,
		FinalSaveAt:      outcome.finalSaveAt,
		Recorded:         outcome.recorded,
		Restorable:       outcome.restorable,
		Abandoned:        outcome.abandoned,
		ElapsedSec:       int64(env.decision.Elapsed.Seconds()),
		IntervalM:        int(env.interval.Minutes()),
		DaemonState:      outcome.daemonState,
		DaemonPhase:      string(outcome.daemonStatus.Phase),
		InhibitorsHeld:   outcome.daemonStatus.InhibitorsHeld,
		LogindDelayMS:    outcome.daemonStatus.LogindDelayMillis,
		ServerManagement: outcome.daemonStatus.ServerManagement,
	}
}

func renderAutosaveDegraded(cmd *cobra.Command, env Env, outcome autosaveOutcome, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, autosavePayload(env, outcome), err)
	}

	if renderErr := renderAutosaveResult(cmd, env, outcome); renderErr != nil {
		return errors.Join(renderErr, err)
	}

	return err
}
