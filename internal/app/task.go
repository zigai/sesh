//go:build linux || darwin

package app

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

const (
	paneEnvironment  = "TMUX_PANE"
	popupWidthShare  = 85
	popupHeightShare = 80
	percent          = 100
	popupBorderCells = 2
	popupAttachVerb  = "popup-attach"
)

var (
	ErrNotInTask      = errors.New("the invoking window is not a task window")
	ErrLedgerRequired = errors.New("task ledger is not configured")
)

// TaskOffer is what the new task popup offers at the caller's pane: the harnesses and, in a project
// session or a repository, the project's name and the locations a task can work in.
type TaskOffer struct {
	Project string
	Offer   task.Offer
}

func (local *Local) registerTaskHandlers() {
	handle(local, action.TaskNew, local.startTask)
	handle(local, action.TaskTab, local.openTab)
	handle(local, action.TaskPopup, local.openTaskPopup)
	handle(local, action.TaskRelayout, local.relayoutTask)
	handle(local, action.TaskSleep, local.sleepTask)
	handle(local, action.TaskWake, local.wakeTask)
	handle(local, action.TaskOpen, local.openTask)
	handle(local, action.TaskFinish, local.finishTask)
	handle(local, action.TaskRun, local.runTaskAction)
}

// TaskOffer lists what a new task can start with at the caller's pane, with branch details when
// describe is set.
func (local *Local) TaskOffer(ctx context.Context, caller action.Caller, describe bool) (TaskOffer, error) {
	offered := TaskOffer{Project: "", Offer: task.Offer{
		Harnesses: nil, Project: "", Locations: nil, Current: -1, NextNumber: 0, Branches: nil, Refs: nil, Starts: nil,
	}}

	service, place, err := local.taskPlace(ctx, caller)
	if err != nil {
		return offered, err
	}

	offered.Offer, err = service.Offer(ctx, place, describe)
	if err != nil {
		return offered, oops.In("app").With("operation", "offer_task").Wrap(err)
	}

	if offered.Offer.Project != "" {
		offered.Project = filepath.Base(offered.Offer.Project)
	}

	return offered, nil
}

// ShowTask moves the invoking client to a task's window and focuses its agent pane.
func (local *Local) ShowTask(ctx context.Context, caller action.Caller, id string) error {
	if local.taskLedger == "" {
		return action.Wrap(action.CodeUnavailable, ErrLedgerRequired)
	}

	navigator, err := local.navigator(caller)
	if err != nil {
		return err
	}

	tasks, err := task.Inventory(ctx, local.server, task.NewStore(local.taskLedger))
	if err != nil {
		return oops.In("app").With("operation", "show_task").Wrap(err)
	}

	index := slices.IndexFunc(tasks, func(live task.Live) bool { return live.Record.ID == id })
	if index < 0 {
		return oops.In("app").With("operation", "show_task").Wrap(task.ErrNotFound)
	}

	live := tasks[index]
	if !live.Present {
		if live, err = local.reopenTask(ctx, caller, live.Record); err != nil {
			return err
		}
	}

	if _, err := navigator.Focus(ctx, agents.FocusTarget{
		PaneID: string(live.AgentPane), SessionID: string(live.SessionID), SessionName: live.SessionName,
	}); err != nil {
		return oops.In("app").With("operation", "show_task").Wrap(err)
	}

	return nil
}

func (local *Local) openTask(ctx context.Context, caller action.Caller, input action.OpenTask) (action.OpenedTask, error) {
	tasks, err := task.Inventory(ctx, local.server, task.NewStore(local.taskLedger))
	if err != nil {
		return action.OpenedTask{ID: input.Task}, oops.In("app").With("operation", "open_task").Wrap(err)
	}

	index := slices.IndexFunc(tasks, func(live task.Live) bool { return live.Record.ID == input.Task })
	if index < 0 {
		return action.OpenedTask{ID: input.Task}, oops.In("app").With("operation", "open_task").Wrap(task.ErrNotFound)
	}

	if !tasks[index].Present {
		_, err = local.reopenTask(ctx, caller, tasks[index].Record)
	}

	return action.OpenedTask{ID: input.Task}, err
}

// reopenTask opens the window of a task that has none: a project task's in its project session,
// a loose task's in the caller's session. It returns where the window is.
func (local *Local) reopenTask(ctx context.Context, caller action.Caller, record task.Record) (task.Live, error) {
	service, err := local.taskService()
	if err != nil {
		return task.Live{}, err
	}

	place, err := local.reopenPlace(ctx, caller, record)
	if err != nil {
		return task.Live{}, err
	}

	if _, err := service.Reopen(ctx, place, record.ID); err != nil {
		return task.Live{}, oops.In("app").With("operation", "open_task").Wrap(err)
	}

	tasks, err := task.Inventory(ctx, local.server, task.NewStore(local.taskLedger))
	if err != nil {
		return task.Live{}, oops.In("app").With("operation", "open_task").Wrap(err)
	}

	index := slices.IndexFunc(tasks, func(live task.Live) bool { return live.Record.ID == record.ID && live.Present })
	if index < 0 {
		return task.Live{}, oops.In("app").With("operation", "open_task").Wrap(task.ErrNotFound)
	}

	return tasks[index], nil
}

func (local *Local) reopenPlace(ctx context.Context, caller action.Caller, record task.Record) (task.Place, error) {
	if record.Kind == task.KindLoose {
		_, place, err := local.taskPlace(ctx, caller)

		return place, err
	}

	return local.projectPlace(ctx, task.Place{Pane: gotmux.PaneID(cmp.Or(caller.Pane, os.Getenv(paneEnvironment))), Session: "", Dir: "", Project: "", Task: ""}, record.Project)
}

func (local *Local) startTask(ctx context.Context, caller action.Caller, input action.NewTask) (action.StartedTask, error) {
	service, place, err := local.taskPlace(ctx, caller)
	if err != nil {
		return action.StartedTask{ID: "", Kind: "", Number: 0, Directory: "", Branch: ""}, err
	}

	if input.Project != "" {
		place, err = local.projectPlace(ctx, place, input.Project)
		if err != nil {
			return action.StartedTask{ID: "", Kind: "", Number: 0, Directory: "", Branch: ""}, err
		}
	}

	record, err := service.Start(ctx, place, task.Request{
		Harness: input.Harness,
		Title:   strings.TrimSpace(input.Title),
		Location: task.Location{
			Where: task.Where(input.Where), Branch: input.Branch, Remote: input.Remote, Directory: input.Directory,
			Task: "", TaskTitle: "",
		},
		Layout: input.Layout,
	})
	started := action.StartedTask{
		ID: record.ID, Kind: string(record.Kind), Number: record.Number, Directory: record.Directory, Branch: record.Branch,
	}

	if err != nil {
		return started, oops.In("app").With("operation", "start_task").Wrap(err)
	}

	return started, nil
}

// projectPlace opens the session of the project at dir, without moving any client to it, and
// returns it as the place a task starts from.
func (local *Local) projectPlace(ctx context.Context, from task.Place, dir string) (task.Place, error) {
	catalog, err := local.projectCatalog()
	if err != nil {
		return from, err
	}

	p, err := catalog.Resolve(ctx, dir)
	if err != nil {
		return from, oops.In("app").With("operation", "resolve_project").Wrap(err)
	}

	opened, err := project.Open(ctx, local.server, p, false)
	if err != nil {
		return from, oops.In("app").With("operation", "open_project").Wrap(err)
	}

	return task.Place{Pane: from.Pane, Session: opened.Session.ID, Dir: p.Root, Project: p.Root, Task: ""}, nil
}

func (local *Local) openTab(ctx context.Context, caller action.Caller, _ action.OpenTab) (action.OpenedTab, error) {
	service, place, err := local.taskPlace(ctx, caller)
	if err != nil {
		return action.OpenedTab{}, err
	}

	if err := service.OpenTab(ctx, place); err != nil {
		return action.OpenedTab{}, oops.In("app").With("operation", "open_tab").Wrap(err)
	}

	return action.OpenedTab{}, nil
}

func (local *Local) relayoutTask(ctx context.Context, caller action.Caller, input action.RelayoutTask) (action.RelaidOutTask, error) {
	service, place, err := local.taskPlace(ctx, caller)
	if err != nil {
		return action.RelaidOutTask{Task: ""}, err
	}

	if place.Task == "" {
		return action.RelaidOutTask{Task: ""}, action.Wrap(action.CodeUnsupported, ErrNotInTask)
	}

	if err := service.Relayout(ctx, place, input.Layout); err != nil {
		return action.RelaidOutTask{Task: place.Task}, oops.In("app").With("operation", "relayout_task").Wrap(err)
	}

	return action.RelaidOutTask{Task: place.Task}, nil
}

func (local *Local) openTaskPopup(ctx context.Context, caller action.Caller, _ action.OpenTaskPopup) (action.ClosedTaskPopup, error) {
	_, place, err := local.taskPlace(ctx, caller)
	if err != nil {
		return action.ClosedTaskPopup{Task: ""}, err
	}

	if place.Task == "" {
		return action.ClosedTaskPopup{Task: ""}, action.Wrap(action.CodeUnsupported, ErrNotInTask)
	}

	result := action.ClosedTaskPopup{Task: place.Task}

	record, err := local.taskRecord(ctx, place.Task)
	if err != nil {
		return result, err
	}

	client, err := local.callerClient(ctx, caller, place)
	if err != nil {
		return result, err
	}

	info, err := client.Info(ctx)
	if err != nil {
		return result, oops.In("app").With("operation", "inspect_popup_client").Wrap(err)
	}

	popup, err := local.openPopupServer()
	if err != nil {
		return result, err
	}

	size := gotmux.Size{
		Width:  max(info.Width*popupWidthShare/percent-popupBorderCells, 1),
		Height: max(info.Height*popupHeightShare/percent-popupBorderCells, 1),
	}
	if _, err := task.EnsurePopupSession(ctx, popup, local.cfg.Tasks, record, size); err != nil {
		return result, oops.In("app").With("operation", "open_task_popup").Wrap(err)
	}

	executable, err := seshExecutable()
	if err != nil {
		return result, err
	}

	var options gotmux.PopupOptions

	options.Program = gotmux.Exec(executable, "task", popupAttachVerb, record.ID, "--socket", local.server.Endpoint().SocketPath)
	options.Dir = record.Directory
	options.Width = strconv.Itoa(popupWidthShare) + "%"
	options.Height = strconv.Itoa(popupHeightShare) + "%"
	options.X = "C"
	options.Y = "C"
	options.CloseOnExit = true

	if err := client.Popup(ctx, options); err != nil {
		return result, oops.In("app").With("operation", "open_task_popup").Wrap(err)
	}

	return result, nil
}

// AttachTaskPopup attaches the terminal to a task's popup session. It runs inside the popup that
// task.popup opens.
func (local *Local) AttachTaskPopup(ctx context.Context, taskID string, streams gotmux.Streams) error {
	popup, err := local.openPopupServer()
	if err != nil {
		return err
	}

	popupSession, err := popup.FindSession(ctx, task.PopupSessionName(taskID))
	if err != nil {
		return oops.In("app").With("operation", "find_task_popup").Wrap(err)
	}

	if err := popupSession.Attach(ctx, streams, gotmux.AttachOptions{
		ReadOnly: false, PreserveEnvironment: false, Detach: gotmux.DetachNone, Dir: "", Flags: nil,
	}); err != nil {
		return oops.In("app").With("operation", "attach_task_popup").Wrap(err)
	}

	return nil
}

// seshExecutable is the sesh binary that tmux runs for task commands, popups and placeholders.
func seshExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", oops.In("app").With("operation", "resolve_executable").Wrap(err)
	}

	return executable, nil
}

func (local *Local) openPopupServer() (*gotmux.Server, error) {
	configFile := "/dev/null"

	if local.cfg.Tasks.PopupConfig != "" {
		expanded, _ := session.ExpandHomePath(local.cfg.Tasks.PopupConfig)
		configFile = expanded
	}

	server, err := tmuxserver.OpenNamed(local.server.Endpoint().SocketPath, local.cfg.Tasks.PopupSocket, configFile)
	if err != nil {
		return nil, oops.In("app").With("operation", "open_popup_server").Wrap(err)
	}

	return server, nil
}

func (local *Local) taskPlace(ctx context.Context, caller action.Caller) (*task.Service, task.Place, error) {
	service, err := local.taskService()
	if err != nil {
		return nil, task.Place{}, err
	}

	pane := caller.Pane
	if pane == "" {
		pane = os.Getenv(paneEnvironment)
	}

	if pane == "" {
		return nil, task.Place{}, action.Wrap(action.CodeUnavailable, action.ErrClientRequired)
	}

	place, err := service.Locate(ctx, gotmux.PaneID(pane))
	if err != nil {
		return nil, task.Place{}, oops.In("app").With("operation", "locate_task_pane").Wrap(err)
	}

	return service, place, nil
}

// callerClient is the client named by the caller, else the only client showing the pane's
// session.
func (local *Local) callerClient(ctx context.Context, caller action.Caller, place task.Place) (gotmux.Client, error) {
	if caller.Client != "" {
		client, err := local.server.Client(ctx, gotmux.ClientName(caller.Client))
		if err != nil {
			return gotmux.Client{}, oops.In("app").With("operation", "resolve_client").Wrap(errors.Join(session.ErrInvokingClientUnavailable, err))
		}

		return client, nil
	}

	clients, err := local.server.Clients(ctx)
	if err != nil {
		return gotmux.Client{}, oops.In("app").With("operation", "resolve_client").Wrap(err)
	}

	var showing []gotmux.ClientInfo

	for _, info := range clients {
		if id, ok := info.SessionID.Get(); ok && id == place.Session && !info.Control {
			showing = append(showing, info)
		}
	}

	if len(showing) != 1 {
		return gotmux.Client{}, session.ErrInvokingClientUnavailable
	}

	return showing[0].Handle(), nil
}
