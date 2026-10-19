//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

const (
	commandNotStarted  = 127
	actionRunVerb      = "action-run"
	defaultPopupWidth  = 120
	defaultPopupHeight = 36
)

var (
	ErrUnknownTaskAction = errors.New("task action is not configured for this task's project")
	ErrPromptFile        = errors.New("task action prompt file cannot be read")
)

// TaskActionRun has a nil ExitCode when the action did not run.
type TaskActionRun struct {
	ExitCode *int
	PR       task.PullRequest
	Linked   bool
}

func (local *Local) finishTask(ctx context.Context, _ action.Caller, input action.FinishTask) (action.FinishedTask, error) {
	service, err := local.taskService()
	if err != nil {
		return action.FinishedTask{ID: input.Task}, err
	}

	record, err := service.Finish(ctx, input.Task, task.FinishOptions{Discard: input.Discard, SkipRun: input.SkipRun})
	if err != nil {
		return action.FinishedTask{ID: input.Task}, oops.In("app").With("operation", "finish_task").Wrap(err)
	}

	if err := local.finishTaskWork(ctx, record, work.FinishedByUser); err != nil {
		return action.FinishedTask{ID: input.Task}, action.Wrap(action.CodePartial, err)
	}

	return action.FinishedTask{ID: input.Task}, nil
}

func (local *Local) runTaskAction(ctx context.Context, _ action.Caller, input action.RunTaskAction) (action.StartedTaskAction, error) {
	result := action.StartedTaskAction{ID: input.Task, Action: input.Action}

	record, err := local.taskRecord(ctx, input.Task)
	if err != nil {
		return result, err
	}

	if _, found := local.cfg.MenuItemsFor(record.Project)[input.Action]; !found {
		return result, action.Wrap(action.CodeNotFound, ErrUnknownTaskAction)
	}

	return result, local.openTaskRunWindow(ctx, record, input.Action, []string{"task", actionRunVerb, record.ID, input.Action})
}

func (local *Local) RunTaskAction(ctx context.Context, id, name string, streams gotmux.Streams) (TaskActionRun, error) {
	run := TaskActionRun{ExitCode: nil, PR: task.PullRequest{Number: 0, State: "", URL: ""}, Linked: false}

	service, err := local.taskService()
	if err != nil {
		return run, err
	}

	record, err := local.taskRecord(ctx, id)
	if err != nil {
		return run, err
	}

	configured, found := local.cfg.MenuItemsFor(record.Project)[name]
	if !found {
		return run, action.Wrap(action.CodeNotFound, ErrUnknownTaskAction)
	}

	argv, err := actionCommand(configured, record)
	if err != nil {
		return run, err
	}

	started := task.Run{Action: name, Window: name, Status: task.RunRunning, ExitCode: 0, StartedAt: time.Now().Unix(), FinishedAt: 0}
	if err := service.RecordRun(ctx, id, started); err != nil {
		return run, oops.In("app").With("operation", "record_task_action").Wrap(err)
	}

	exitCode := runCommand(ctx, argv, record, streams)
	run.ExitCode = &exitCode

	finished := started
	finished.Status, finished.ExitCode, finished.FinishedAt = task.RunOK, exitCode, time.Now().Unix()

	if exitCode != 0 {
		finished.Status = task.RunFailed
	}

	if err := service.RecordRun(context.WithoutCancel(ctx), id, finished); err != nil {
		return run, oops.In("app").With("operation", "record_task_action").Wrap(err)
	}

	if configured.LinksPR() {
		refreshed, _, err := service.RefreshPR(ctx, id)
		if err != nil {
			return run, oops.In("app").With("operation", "link_task_pr").Wrap(err)
		}

		run.PR, run.Linked = refreshed.PR, refreshed.PR.Number != 0
	}

	return run, nil
}

func (local *Local) SyncTasks(ctx context.Context, titles map[string]string) (bool, error) {
	service, err := local.taskService()
	if err != nil {
		return false, err
	}

	ledger, err := task.NewStore(local.taskLedger).Load(ctx)
	if err != nil {
		return false, oops.In("app").With("operation", "load_tasks").Wrap(err)
	}

	changed := false
	findPRs := true

	for _, record := range ledger.Tasks {
		if findPRs {
			_, refreshed, err := service.RefreshPR(ctx, record.ID)
			changed = changed || refreshed
			findPRs = !errors.Is(err, task.ErrGitHubFailed)
		}

		if title := titles[record.ID]; title != "" {
			renamed, err := service.Retitle(ctx, record.ID, title)
			if err != nil && !errors.Is(err, task.ErrWindowMissing) {
				return changed, oops.In("app").With("operation", "retitle_task").Wrap(err)
			}

			changed = changed || renamed
		}
	}

	return changed, nil
}

func (local *Local) taskRecord(ctx context.Context, id string) (task.Record, error) {
	if local.taskLedger == "" {
		return task.EmptyRecord(), action.Wrap(action.CodeUnavailable, ErrLedgerRequired)
	}

	ledger, err := task.NewStore(local.taskLedger).Load(ctx)
	if err != nil {
		return task.EmptyRecord(), oops.In("app").With("operation", "load_tasks").Wrap(err)
	}

	record, found := ledger.Find(id)
	if !found {
		return task.EmptyRecord(), action.Wrap(action.CodeNotFound, task.ErrNotFound)
	}

	return record, nil
}

func (local *Local) closeTaskPopup(ctx context.Context, id string) error {
	popup, err := local.openPopupServer()
	if err != nil {
		return err
	}

	popupSession, err := popup.FindSession(ctx, task.PopupSessionName(id))
	if errors.Is(err, gotmux.ErrNotFound) || errors.Is(err, gotmux.ErrNoServer) {
		return nil
	}

	if err != nil {
		return oops.In("app").With("operation", "find_task_popup").Wrap(err)
	}

	if err := popupSession.Kill(ctx); err != nil && !errors.Is(err, gotmux.ErrNotFound) {
		return oops.In("app").With("operation", "close_task_popup").Wrap(err)
	}

	return nil
}

func actionCommand(configured config.MenuItemConfig, record task.Record) ([]string, error) {
	argv := append([]string{}, configured.Run...)

	prompt := configured.Prompt
	if configured.PromptFile != "" {
		path, _ := session.ExpandHomePath(configured.PromptFile)
		if !filepath.IsAbs(path) {
			path = filepath.Join(record.Directory, path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, action.Wrap(action.CodeInvalidArgument, errors.Join(ErrPromptFile, err))
		}

		prompt = string(data)
		if err := config.ValidateTaskActionTemplate(prompt); err != nil {
			return nil, action.Wrap(action.CodeInvalidArgument, err)
		}
	}

	if prompt != "" {
		argv = append(argv, task.Render(prompt, record))
	}

	return argv, nil
}

func runCommand(ctx context.Context, argv []string, record task.Record, streams gotmux.Streams) int {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = record.Directory
	command.Stdin, command.Stdout, command.Stderr = streams.In, streams.Out, streams.Err
	command.Env = os.Environ()

	for name, value := range task.Env(record) {
		command.Env = append(command.Env, name+"="+value)
	}

	err := command.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}

	if err != nil {
		if streams.Err != nil {
			_, _ = fmt.Fprintln(streams.Err, err)
		}

		return commandNotStarted
	}

	return 0
}
