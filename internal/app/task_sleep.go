//go:build linux || darwin

package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/task"
)

type paneAgents struct {
	source agents.SessionSource
	socket string
}

func (local *Local) sleepTask(ctx context.Context, _ action.Caller, input action.SleepTask) (action.SleptTask, error) {
	service, err := local.taskService()
	if err != nil {
		return action.SleptTask{ID: ""}, err
	}

	record, err := service.Sleep(ctx, input.Task)
	if err != nil {
		return action.SleptTask{ID: record.ID}, oops.In("app").With("operation", "sleep_task").Wrap(err)
	}

	return action.SleptTask{ID: record.ID}, nil
}

func (local *Local) wakeTask(ctx context.Context, _ action.Caller, input action.WakeTask) (action.WokenTask, error) {
	service, err := local.taskService()
	if err != nil {
		return action.WokenTask{ID: "", AlreadyAwake: false}, err
	}

	woken, err := service.Wake(ctx, input.Task)
	result := action.WokenTask{ID: woken.Record.ID, AlreadyAwake: woken.AlreadyAwake}

	if err != nil {
		return result, oops.In("app").With("operation", "wake_task").Wrap(err)
	}

	return result, nil
}

func (local *Local) WakeTaskHere(ctx context.Context, id string) ([]string, error) {
	service, err := local.taskService()
	if err != nil {
		return nil, err
	}

	woken, err := service.WakeHere(ctx, id)
	if err != nil {
		return nil, oops.In("app").With("operation", "wake_task").Wrap(err)
	}

	if woken.AlreadyAwake {
		return nil, nil
	}

	return woken.Command, nil
}

func (local *Local) TaskHarness(ctx context.Context, id string) (string, error) {
	record, err := local.taskRecord(ctx, id)

	return record.Harness, err
}

func (local *Local) selectTask(ctx context.Context, selector string) (string, error) {
	if !strings.HasPrefix(selector, "%") {
		return selector, nil
	}

	id, err := local.paneTask(ctx, selector)
	if err == nil && id == "" {
		err = action.Wrap(action.CodeInvalidArgument, action.ErrTaskRequired)
	}

	return id, err
}

func (local *Local) paneTask(ctx context.Context, pane string) (string, error) {
	panes, err := local.server.PanesWith(ctx, gotmux.QueryOptions{
		Filter: gotmux.Format("#{==:#{pane_id}," + pane + "}"), ExtraFields: []string{task.OptionTask},
	})
	if err != nil {
		return "", oops.In("app").With("operation", "find_pane_task").Wrap(err)
	}

	if len(panes) != 1 {
		return "", action.Wrap(action.CodeNotFound, agents.ErrPaneNotLive)
	}

	id, _ := panes[0].Raw(task.OptionTask)

	return string(id), nil
}

func (local *Local) taskService() (*task.Service, error) {
	if local.server == nil {
		return nil, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	if local.taskLedger == "" {
		return nil, action.Wrap(action.CodeUnavailable, ErrLedgerRequired)
	}

	catalog, err := project.NewCatalog(local.cfg.Projects)
	if err != nil {
		return nil, action.Wrap(action.CodeInvalidArgument, err)
	}

	executable, err := seshExecutable()
	if err != nil {
		return nil, err
	}

	socket := local.server.Endpoint().SocketPath

	return task.NewService(task.Options{
		Server: local.server, Store: task.NewStore(local.taskLedger), Catalog: catalog, Config: local.cfg.Tasks,
		Windows:     local.cfg.TaskWindowFor,
		WorktreeDir: local.worktreeDir, LookPath: exec.LookPath, Now: time.Now,
		Agents: paneAgents{source: local.agentSource, socket: agents.NormalizeServerSocket(socket)},
		SleepingAgentCommand: func(id string) []string {
			return append([]string{executable}, apps.PlaceholderArguments(id)...)
		},
		PRs: task.GitHub{}, ClosePopup: local.closeTaskPopup, FinishCommand: local.cfg.FinishRunFor,
	}), nil
}

func (finder paneAgents) AgentsIn(ctx context.Context, pane gotmux.PaneID) ([]task.Agent, error) {
	if finder.source == nil {
		return nil, oops.In("app").With("operation", "find_task_agent").Wrap(agents.ErrRegistryUnavailable)
	}

	var filter aht.Filter

	filter.Presence = aht.PresenceLive
	filter.MultiplexerPane = string(pane)

	sessions, err := finder.source.List(ctx, filter)
	if err != nil {
		return nil, oops.In("app").With("operation", "find_task_agent").Wrap(err)
	}

	found := make([]task.Agent, 0, 1)

	for _, observed := range sessions {
		location := observed.Location
		if location.Kind != aht.MultiplexerTmux || location.PaneID != string(pane) || len(observed.ResumeCommand) == 0 ||
			agents.NormalizeServerSocket(location.ServerID) != finder.socket {
			continue
		}

		found = append(found, taskAgent(observed))
	}

	return found, nil
}

func taskAgent(observed aht.Session) task.Agent {
	agent := task.Agent{
		Harness: string(observed.Harness), Activity: "", SessionID: observed.SessionID, SessionPath: observed.SessionPath,
		ResumeCommand: observed.ResumeCommand, Executable: "", PanePID: observed.Location.PanePID,
	}

	if activity := observed.Activity(); activity != nil {
		agent.Activity = string(*activity)
	}

	if len(observed.ResumeCommand) > 0 {
		if executable, err := exec.LookPath(observed.ResumeCommand[0]); err == nil && filepath.IsAbs(executable) {
			agent.Executable = filepath.Clean(executable)
		}
	}

	return agent
}
