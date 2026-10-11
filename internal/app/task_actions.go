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

// TaskActionRun is the result of running a task action in its popup window. ExitCode is only
// meaningful when the action Ran.
type TaskActionRun struct {
	Ran      bool
	ExitCode int
	PR       task.PullRequest
	Linked   bool
}

func (local *Local) finishTask(ctx context.Context, _ action.Caller, input action.FinishTask) (action.FinishedTask, error) {
	service, err := local.taskService()
	if err != nil {
		return action.FinishedTask{ID: input.Task}, err
	}

	if _, err := service.Finish(ctx, input.Task, input.Discard); err != nil {
		return action.FinishedTask{ID: input.Task}, oops.In("app").With("operation", "finish_task").Wrap(err)
	}

	return action.FinishedTask{ID: input.Task}, nil
}

// runTaskAction opens a window in the task's popup session that runs the action through the
// hidden action-run command, and selects it so the popup shows its output.
func (local *Local) runTaskAction(ctx context.Context, _ action.Caller, input action.RunTaskAction) (action.StartedTaskAction, error) {
	result := action.StartedTaskAction{ID: input.Task, Action: input.Action}

	record, err := local.taskRecord(ctx, input.Task)
	if err != nil {
		return result, err
	}

	if _, found := local.cfg.TaskActionsFor(projectName(record))[input.Action]; !found {
		return result, action.Wrap(action.CodeNotFound, ErrUnknownTaskAction)
	}

	popup, err := local.openPopupServer()
	if err != nil {
		return result, err
	}

	popupSession, err := task.EnsurePopupSession(ctx, popup, local.cfg.Tasks, record,
		gotmux.Size{Width: defaultPopupWidth, Height: defaultPopupHeight})
	if err != nil {
		return result, oops.In("app").With("operation", "open_task_popup").Wrap(err)
	}

	executable, err := seshExecutable()
	if err != nil {
		return result, err
	}

	argv := []string{executable, "task", actionRunVerb, record.ID, input.Action, "--socket", local.server.Endpoint().SocketPath}

	source := local.cfg.Source()
	if source.WithoutConfig {
		argv = append(argv, "--no-config")
	} else if source.Path != "" {
		argv = append(argv, "--config", source.Path)
	}

	if source.Path == "-" {
		argv = append([]string{"/bin/sh", "-c", `config=$1; shift; printf '%s' "$config" | "$@"`, "task-action", source.Input}, argv...)
	}

	if err := task.OpenRunWindow(ctx, popup, popupSession, record, input.Action, argv); err != nil {
		return result, oops.In("app").With("operation", "open_task_action").Wrap(err)
	}

	return result, nil
}

// RunTaskAction runs a task action in the current terminal, records its run on the task, and
// links the branch's pull request afterwards when the action asks for it.
func (local *Local) RunTaskAction(ctx context.Context, id, name string, streams gotmux.Streams) (TaskActionRun, error) {
	run := TaskActionRun{Ran: false, ExitCode: 0, PR: task.PullRequest{Number: 0, State: "", URL: ""}, Linked: false}

	service, err := local.taskService()
	if err != nil {
		return run, err
	}

	record, err := local.taskRecord(ctx, id)
	if err != nil {
		return run, err
	}

	configured, found := local.cfg.TaskActionsFor(projectName(record))[name]
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

	run.Ran, run.ExitCode = true, runCommand(ctx, argv, record, streams)

	finished := started
	finished.Status, finished.ExitCode, finished.FinishedAt = task.RunOK, run.ExitCode, time.Now().Unix()

	if run.ExitCode != 0 {
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

// SyncTasks records the current pull request of every project task and renames task windows
// after the titles their agents report, keyed by task. It reports whether any record changed.
// Without gh, pull requests are left as they are.
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

// actionCommand is the command line of a task action: its run command and, when it has one,
// its prompt rendered for the task as the last argument.
func actionCommand(configured config.TaskActionConfig, record task.Record) ([]string, error) {
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

// runCommand runs argv in the task's worktree on the terminal and returns its exit code; a
// command that cannot start reports 127 like a shell would.
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

func projectName(record task.Record) string {
	if record.Kind != task.KindProject {
		return ""
	}

	return filepath.Base(record.Project)
}
