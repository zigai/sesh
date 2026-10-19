//go:build linux || darwin

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/task"
)

const mergeRunVerb = "merge-run"

var ErrPaneMissing = errors.New("the agent's pane is gone")

type TaskMergeRun struct {
	ExitCode *int
	Merged   task.Merged
	Finished bool
}

func (local *Local) mergeTask(ctx context.Context, _ action.Caller, input action.MergeTask) (action.StartedTaskMerge, error) {
	result := action.StartedTaskMerge{ID: input.Task}

	record, err := local.taskRecord(ctx, input.Task)
	if err != nil {
		return result, err
	}

	if record.Kind != task.KindProject {
		return result, action.Wrap(action.CodeInvalidArgument, task.ErrNotProjectTask)
	}

	return result, local.openTaskRunWindow(ctx, record, task.MergeAction, []string{"task", mergeRunVerb, record.ID})
}

// RunTaskMerge merges a task in the current terminal, records the run on the task, and finishes
// the task once its branch is on the base. The finish closes the popup session this runs in, so
// it runs in its own session and the hangup is ignored until it ends.
func (local *Local) RunTaskMerge(ctx context.Context, id string, streams gotmux.Streams) (TaskMergeRun, error) {
	run := TaskMergeRun{ExitCode: nil, Merged: task.Merged{Branch: "", Base: ""}, Finished: false}

	service, err := local.taskService()
	if err != nil {
		return run, err
	}

	record, err := local.taskRecord(ctx, id)
	if err != nil {
		return run, err
	}

	started := task.Run{
		Action: task.MergeAction, Window: task.MergeAction, Status: task.RunRunning, ExitCode: 0,
		StartedAt: time.Now().Unix(), FinishedAt: 0,
	}
	if err := service.RecordRun(ctx, id, started); err != nil {
		return run, oops.In("app").With("operation", "record_task_merge").Wrap(err)
	}

	var mergeErr, finishErr error

	run.Merged, mergeErr = task.MergeBranch(ctx, record.Directory, record.Base, local.cfg.MergeCheckFor(record.Project), streams.Out)
	exitCode := task.MergeExitCode(mergeErr)
	run.ExitCode = &exitCode

	if mergeErr == nil {
		_, _ = fmt.Fprintf(streams.Out, "· merged %s into %s; finishing the task\n", run.Merged.Branch, run.Merged.Base)

		signal.Ignore(syscall.SIGHUP)

		// The finish removes the task with its runs, so the run stays running until it ends.
		if finishErr = local.finishInOwnSession(ctx, id); finishErr == nil {
			run.Finished = true

			return run, nil
		}
	}

	finished := started
	finished.Status, finished.ExitCode, finished.FinishedAt = task.RunOK, exitCode, time.Now().Unix()

	if mergeErr != nil {
		finished.Status = task.RunFailed
	}

	if err := service.RecordRun(context.WithoutCancel(ctx), id, finished); err != nil {
		return run, errors.Join(mergeErr, finishErr, oops.In("app").With("operation", "record_task_merge").Wrap(err))
	}

	if mergeErr != nil {
		return run, oops.In("app").With("operation", "merge_task").Wrap(mergeErr)
	}

	return run, oops.In("app").With("operation", "finish_merged_task").Wrap(finishErr)
}

// finishInOwnSession finishes a task through a child sesh in a new session. When the popup
// terminal closes, its whole foreground job is hung up, tmux clients that sesh starts included;
// a finish that belonged to that job would lose the reply to its own kill-session.
func (local *Local) finishInOwnSession(ctx context.Context, id string) error {
	argv, err := local.seshCommand([]string{"task", "finish", id})
	if err != nil {
		return err
	}

	var (
		stderr      bytes.Buffer
		processAttr syscall.SysProcAttr
	)

	processAttr.Setsid = true

	command := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	command.SysProcAttr = &processAttr
	command.Stderr = &stderr

	if source := local.cfg.Source(); source.Path == "-" {
		command.Stdin = strings.NewReader(source.Input)
	}

	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return nil
}

func (local *Local) mergeAgentBranch(ctx context.Context, _ action.Caller, input action.MergeAgentBranch) (action.MergedAgentBranch, error) {
	result := action.MergedAgentBranch{Branch: "", Base: ""}

	panes, err := local.server.PanesWith(ctx, gotmux.QueryOptions{
		Filter: gotmux.Format("#{==:#{pane_id}," + input.Pane + "}"), ExtraFields: nil,
	})
	if err != nil {
		return result, oops.In("app").With("operation", "find_agent_pane").Wrap(err)
	}

	if len(panes) != 1 {
		return result, action.Wrap(action.CodeNotFound, ErrPaneMissing)
	}

	directory := panes[0].CurrentPath

	merged, err := task.MergeBranch(ctx, directory, "", local.cfg.MergeCheckFor(directory), io.Discard)
	result.Branch, result.Base = merged.Branch, merged.Base

	if err != nil {
		return result, oops.In("app").With("operation", "merge_agent_branch").Wrap(err)
	}

	return result, nil
}

func (local *Local) openTaskRunWindow(ctx context.Context, record task.Record, name string, args []string) error {
	popup, err := local.openPopupServer()
	if err != nil {
		return err
	}

	popupSession, err := task.EnsurePopupSession(ctx, popup, local.cfg.Tasks, record,
		gotmux.Size{Width: defaultPopupWidth, Height: defaultPopupHeight})
	if err != nil {
		return oops.In("app").With("operation", "open_task_popup").Wrap(err)
	}

	argv, err := local.seshCommand(args)
	if err != nil {
		return err
	}

	if source := local.cfg.Source(); source.Path == "-" {
		argv = append([]string{"/bin/sh", "-c", `config=$1; shift; printf '%s' "$config" | "$@"`, "task-action", source.Input}, argv...)
	}

	if err := task.OpenRunWindow(ctx, popup, popupSession, record, name, argv); err != nil {
		return oops.In("app").With("operation", "open_task_run").Wrap(err)
	}

	return nil
}

func (local *Local) seshCommand(args []string) ([]string, error) {
	executable, err := seshExecutable()
	if err != nil {
		return nil, err
	}

	argv := append([]string{executable}, args...)
	argv = append(argv, "--socket", local.server.Endpoint().SocketPath)

	source := local.cfg.Source()
	if source.WithoutConfig {
		argv = append(argv, "--no-config")
	} else if source.Path != "" {
		argv = append(argv, "--config", source.Path)
	}

	return argv, nil
}
