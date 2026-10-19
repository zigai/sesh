package task

import (
	"context"
	"errors"
	"fmt"

	gotmux "github.com/zigai/gotmux/tmux"
)

const socketPathField = "socket_path"

// Live is a task record and where its window is in tmux. A task without a tagged window is
// not Present; its record is kept and never matched to a window by guessing.
type Live struct {
	Record      Record
	Present     bool
	SessionID   gotmux.SessionID
	SessionName string
	WindowID    gotmux.WindowID
	WindowIndex int
	WindowName  string
	AgentPane   gotmux.PaneID
	AgentPID    int
	Stalled     bool
}

type PaneTasks struct {
	Socket     string
	TaskByPane map[string]string
}

func Inventory(ctx context.Context, server *gotmux.Server, store Store) ([]Live, error) {
	ledger, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}

	located, err := locateTasks(ctx, server)
	if err != nil {
		return nil, taskError("list_tasks", err)
	}

	live := make([]Live, 0, len(ledger.Tasks))

	for _, record := range ledger.Tasks {
		entry, found := located[record.ID]
		if !found {
			entry = absent()
		}

		entry.Record = record
		entry.Stalled = Stalled(ctx, record)
		live = append(live, entry)
	}

	return live, nil
}

func WindowPanes(ctx context.Context, server *gotmux.Server) (PaneTasks, error) {
	found := PaneTasks{Socket: "", TaskByPane: map[string]string{}}

	panes, err := server.PanesWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{OptionTask, socketPathField}})
	if errors.Is(err, gotmux.ErrNoServer) {
		return found, nil
	}

	if err != nil {
		return found, fmt.Errorf("list task panes: %w", err)
	}

	for _, pane := range panes {
		if path, _ := pane.Raw(socketPathField); len(path) > 0 {
			found.Socket = string(path)
		}

		if taskID, _ := pane.Raw(OptionTask); len(taskID) > 0 {
			found.TaskByPane[string(pane.ID)] = string(taskID)
		}
	}

	return found, nil
}

func locateTasks(ctx context.Context, server *gotmux.Server) (map[string]Live, error) {
	panes, err := server.PanesWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{OptionTask, OptionRole}})
	if errors.Is(err, gotmux.ErrNoServer) {
		return map[string]Live{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list task panes: %w", err)
	}

	located := make(map[string]Live)

	for _, pane := range panes {
		taskID, _ := pane.Raw(OptionTask)
		if len(taskID) == 0 {
			continue
		}

		entry, seen := located[string(taskID)]
		if !seen {
			entry = absent()
			entry.Present = true
			entry.WindowID = pane.WindowID
			entry.SessionID, _ = pane.SessionID.Get()
			entry.SessionName, _ = pane.SessionName.Get()
			entry.WindowIndex, _ = pane.WindowIndex.Get()
			entry.WindowName, _ = pane.WindowName.Get()
		}

		if role, _ := pane.Raw(OptionRole); string(role) == RoleAgent {
			entry.AgentPane = pane.ID
			entry.AgentPID = pane.PID
		}

		located[string(taskID)] = entry
	}

	return located, nil
}

func absent() Live {
	return Live{
		Record: EmptyRecord(), Present: false, SessionID: "", SessionName: "", WindowID: "", WindowIndex: 0,
		WindowName: "", AgentPane: "", AgentPID: 0, Stalled: false,
	}
}
