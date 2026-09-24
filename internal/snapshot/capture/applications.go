package capture

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func ObserveWithApplicationRecovery(ctx context.Context, server *gotmux.Server, selected []gotmux.SessionID, envKeys []string, source agents.SessionSource) (Observation, error) {
	if err := process.ValidateEnvironmentKeys(envKeys); err != nil {
		return Observation{}, captureError(err)
	}

	observation, err := observe(ctx, server, selected, envKeys, true, source)

	return observation, captureError(err)
}

func captureProcessLaunch(ctx context.Context, pid int, keys []string, agent *apps.AgentResume) (model.Launch, paneProcessRecord, error) {
	var (
		record paneProcessRecord
		launch model.Launch
	)

	birth, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		if errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrIdentityMismatch) {
			return launch, record, errors.Join(ErrChanged, oops.In("snapshot.capture").With("operation", "read_process_identity").Wrap(err))
		}

		return launch, record, oops.In("snapshot.capture").With("operation", "read_process_identity").Wrap(err)
	}

	observed, unsupported, err := observeForegroundOrRoot(ctx, pid, birth, keys)
	if err != nil {
		return launch, record, err
	}

	if observed.ExecutableDeleted || observed.DirectoryDeleted {
		record = paneProcessRecord{pid: pid, birth: birth, foreground: observed, dead: false}
		launch = processLaunch(observed)
		launch.Recovery = model.RecoveryNonrestartable

		if observed.ExecutableDeleted {
			launch.Reason = string(model.ReasonExecutableDeleted)
		} else {
			launch.Reason = string(model.ReasonWorkingDirectoryDeleted)
		}

		return launch, record, nil
	}

	record = paneProcessRecord{pid: pid, birth: birth, foreground: nil, dead: false}

	launch = processLaunch(observed)
	if unsupported {
		return launch, record, nil
	}

	record.foreground = observed
	launch, err = prepareApplication(ctx, observed, agent)

	return launch, record, err
}

func sameProcessObservation(left, right *process.Observation) bool {
	return left.PID == right.PID && left.Birth == right.Birth &&
		bytes.Equal(left.Executable, right.Executable) && bytes.Equal(left.Directory, right.Directory) &&
		slices.EqualFunc(left.Arguments, right.Arguments, bytes.Equal)
}

func recoveryAgents(ctx context.Context, source agents.SessionSource) ([]agents.AgentSession, error) {
	if source == nil {
		return nil, captureError(ctx.Err())
	}

	live, err := agents.LoadLiveAgents(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(ctx.Err(), err)
		}

		return nil, nil
	}

	return live, nil
}

func observeForegroundOrRoot(ctx context.Context, pid int, birth process.Identity, keys []string) (*process.Observation, bool, error) {
	observed, err := process.ObserveForeground(ctx, process.ForegroundRequest{
		PanePID:           pid,
		ExpectedPaneBirth: birth,
		EnvironmentKeys:   keys,
	})

	unsupported := !errors.Is(err, process.ErrForegroundChanged) &&
		(errors.Is(err, process.ErrForegroundAmbiguous) || errors.Is(err, process.ErrForegroundUnavailable))
	if unsupported {
		observed, err = process.Observe(ctx, process.Request{
			PID:             pid,
			ExpectedBirth:   birth,
			EnvironmentKeys: keys,
		})
	}

	if err != nil {
		if errors.Is(err, process.ErrForegroundChanged) || errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrIdentityMismatch) {
			return nil, false, errors.Join(ErrChanged, captureError(err))
		}

		return nil, false, captureError(err)
	}

	return observed, unsupported, nil
}

func processLaunch(observed *process.Observation) model.Launch {
	args := make([]model.Bytes, len(observed.Arguments))
	for i, arg := range observed.Arguments {
		args[i] = model.Bytes(bytes.Clone(arg))
	}

	return model.Launch{
		Executable:  model.Bytes(bytes.Clone(observed.Executable)),
		Arguments:   args,
		Directory:   model.Bytes(bytes.Clone(observed.Directory)),
		Environment: observed.Environment,
		Birth:       model.Bytes(observed.Birth.Bytes()),
		Recovery:    model.RecoveryUnverified,
		Reason:      "",
	}
}

func prepareApplication(ctx context.Context, observed *process.Observation, agent *apps.AgentResume) (model.Launch, error) {
	launch := processLaunch(observed)

	enriched, reason, err := apps.EnrichEditor(ctx, launch, observed.PID)
	if ctx.Err() != nil {
		return launch, errors.Join(ctx.Err(), err)
	}

	if errors.Is(err, apps.ErrEditorIdentityChanged) {
		return launch, errors.Join(ErrChanged, err)
	}

	if err == nil && reason.Supported() {
		return enriched, nil
	}

	prepared, _ := apps.Prepare(launch, agent)

	return prepared, nil
}

func matchingAgentResume(live []agents.AgentSession, endpoint string, pane gotmux.PaneInfo) *apps.AgentResume {
	var found *agents.AgentSession

	for i := range live {
		candidate := &live[i]
		if !filepath.IsAbs(candidate.ServerSocket) || filepath.Clean(candidate.ServerSocket) != endpoint ||
			candidate.PaneID != string(pane.ID) || candidate.PanePID != pane.PID {
			continue
		}

		if found != nil {
			return nil
		}

		found = candidate
	}

	if found == nil || len(found.ResumeCommand) == 0 {
		return nil
	}

	executable, err := exec.LookPath(found.ResumeCommand[0])
	if err != nil || !filepath.IsAbs(executable) {
		executable = ""
	} else {
		executable = filepath.Clean(executable)
	}

	return &apps.AgentResume{
		Harness:     found.Harness,
		SessionID:   found.HarnessSessionID,
		SessionPath: found.SessionPath,
		Command:     found.ResumeCommand,
		Executable:  executable,
	}
}
