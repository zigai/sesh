//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

var (
	ErrWorkStateRequired = errors.New("work state is not configured")
	ErrWorkDirectoryGone = errors.New("the conversation's directory no longer exists")
)

// resumeAgent opens a window that resumes a recorded conversation in its directory: in its
// project's session, else in the invoking pane's session, and reopens it.
func (local *Local) resumeAgent(ctx context.Context, caller action.Caller, input action.ResumeAgent) (action.ResumedAgent, error) {
	var resumed action.ResumedAgent

	resumed.Item = input.Item

	item, err := local.workItem(ctx, input.Item)
	if err != nil {
		return resumed, err
	}

	if _, err := os.Stat(item.Directory); err != nil {
		return resumed, action.Wrap(action.CodeUnavailable, fmt.Errorf("%w: %w", ErrWorkDirectoryGone, err))
	}

	var place task.Place
	if item.Project != "" {
		place, err = local.projectPlace(ctx, task.Place{Pane: gotmux.PaneID(caller.Pane), Session: "", Dir: "", Project: "", Task: ""}, item.Project)
	} else {
		_, place, err = local.taskPlace(ctx, caller)
	}

	if err != nil {
		return resumed, err
	}

	pane, err := task.RunWindow(ctx, local.server, place.Session, item.Directory, item.Resume)
	if err != nil {
		return resumed, oops.In("app").With("operation", "resume_agent").Wrap(err)
	}

	row, err := local.findLive(ctx, string(place.Session))
	if err != nil {
		return resumed, err
	}

	resumed.Session = action.RefOf(row)
	resumed.Pane = string(pane)

	if item.Finished() {
		if err := local.updateWork(ctx, func(state *work.State) error { return state.Reopen(item.ID) }); err != nil {
			return resumed, action.Wrap(action.CodePartial, err)
		}
	}

	return resumed, nil
}

func (local *Local) finishWork(ctx context.Context, _ action.Caller, input action.ChangeWork) (action.ChangedWork, error) {
	return action.ChangedWork{Item: input.Item}, local.updateWork(ctx, func(state *work.State) error {
		return state.Finish(input.Item, work.FinishedByUser, time.Now())
	})
}

func (local *Local) pinWork(ctx context.Context, _ action.Caller, input action.ChangeWork) (action.ChangedWork, error) {
	return action.ChangedWork{Item: input.Item}, local.updateWork(ctx, func(state *work.State) error {
		return state.Pin(input.Item, time.Now())
	})
}

func (local *Local) unpinWork(ctx context.Context, _ action.Caller, input action.ChangeWork) (action.ChangedWork, error) {
	return action.ChangedWork{Item: input.Item}, local.updateWork(ctx, func(state *work.State) error {
		return state.Unpin(input.Item)
	})
}

// linkWork links a pull request of the conversation's repository, as gh reports it.
func (local *Local) linkWork(ctx context.Context, _ action.Caller, input action.ChangeWork) (action.ChangedWork, error) {
	changed := action.ChangedWork{Item: input.Item}

	item, err := local.workItem(ctx, input.Item)
	if err != nil {
		return changed, err
	}

	pr, err := work.ViewPR(ctx, item.Directory, input.PR)
	if err != nil {
		return changed, action.Wrap(action.CodeUnavailable, oops.In("app").With("operation", "view_pr").Wrap(err))
	}

	return changed, local.updateWork(ctx, func(state *work.State) error { return state.Link(input.Item, pr) })
}

func (local *Local) unlinkWork(ctx context.Context, _ action.Caller, input action.ChangeWork) (action.ChangedWork, error) {
	return action.ChangedWork{Item: input.Item}, local.updateWork(ctx, func(state *work.State) error {
		return state.Unlink(input.Item, input.PR)
	})
}

// finishTaskWork finishes the recorded conversations of a task that was finished.
func (local *Local) finishTaskWork(ctx context.Context, id string) error {
	if local.workState == "" {
		return nil
	}

	_, err := work.NewStore(local.workState).Update(ctx, func(state *work.State) error {
		now := time.Now()

		for _, item := range state.Items {
			if item.Task == id && !item.Finished() {
				if err := state.Finish(item.ID, work.FinishedByUser, now); err != nil {
					return fmt.Errorf("finish work item: %w", err)
				}
			}
		}

		return nil
	})
	if err != nil {
		return oops.In("app").With("operation", "finish_task_work").Wrap(err)
	}

	return nil
}

func (local *Local) updateWork(ctx context.Context, change func(*work.State) error) error {
	if local.workState == "" {
		return action.Wrap(action.CodeUnavailable, ErrWorkStateRequired)
	}

	_, err := work.NewStore(local.workState).Update(ctx, change)

	switch {
	case errors.Is(err, work.ErrNotFound), errors.Is(err, work.ErrNotLinked):
		return action.Wrap(action.CodeNotFound, err)
	case err != nil:
		return oops.In("app").With("operation", "update_work").Wrap(err)
	default:
		return nil
	}
}

func (local *Local) workItem(ctx context.Context, id string) (work.Item, error) {
	if local.workState == "" {
		return work.EmptyItem(), action.Wrap(action.CodeUnavailable, ErrWorkStateRequired)
	}

	state, err := work.NewStore(local.workState).Load(ctx)
	if err != nil {
		return work.EmptyItem(), oops.In("app").With("operation", "load_work").Wrap(err)
	}

	item, found := state.Find(id)
	if !found {
		return work.EmptyItem(), action.Wrap(action.CodeNotFound, work.ErrNotFound)
	}

	return item, nil
}
