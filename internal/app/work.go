//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"os"

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
// project's session, else in the invoking pane's session.
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

	return resumed, nil
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
