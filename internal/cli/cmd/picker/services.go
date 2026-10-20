//go:build linux || darwin

package picker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/history"
	"github.com/zigai/aht/v2/pkg/registry"
	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sync/singleflight"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui"
	"github.com/zigai/sesh/internal/work"
)

var errPickerTargetChanged = errors.New("selected session changed since it was observed")

type RemoteServices struct {
	Catalog      tui.RemoteCatalogSource
	Watcher      tui.RemoteCatalogWatcher
	Peer         action.Dispatcher
	Attach       tui.RemoteAttacher
	OriginBridge tui.OriginBridgeService
	Origin       action.Dispatcher
}

type RemoteFactory func(context.Context, *config.Config, string) (RemoteServices, error)

type services struct {
	workspace        app.Workspace
	server           *gotmux.Server
	currentPane      string
	currentSessionID string
	popupChild       bool
	repository       *app.Repository
	agentStore       agents.SnapshotSource
	sessionNames     transcriptNameCache
	titles           titleCache
	panes            singleflight.Group
	initialReads     *initialReads
	workflow         *session.Workflow
	actions          action.Router
	local            *app.Local
	remote           RemoteServices
	viewsMu          sync.Mutex
	deferredViews    []tui.AgentView
}

func AgentSummaries(observed []agents.AgentSession) []session.AgentSummary {
	return agentSummaries(observed)
}

func newServices(
	ctx context.Context,
	workspace app.Workspace,
	streams gotmux.Streams,
	remote RemoteServices,
) (*services, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return nil, err
	}

	agentStore := workspace.AgentSource()
	repository := app.NewRepository(workspace.Repository)
	local := app.NewLocal(app.LocalOptions{
		Config:             workspace.Config,
		Server:             server,
		Repository:         repository,
		LeaseRoot:          workspace.LeaseRoot,
		HistoryPath:        workspace.History,
		AgentSource:        agentStore,
		Streams:            streams,
		Source:             "picker",
		DeferSwitchHistory: true,
		TaskLedger:         workspace.Tasks,
		WorkState:          workspace.Work,
		WorktreeDir:        workspace.WorktreeDir,
	})

	return &services{
		workspace:        workspace,
		server:           server,
		currentPane:      "",
		currentSessionID: "",
		popupChild:       false,
		repository:       repository,
		agentStore:       agentStore,
		sessionNames:     transcriptNameCache{mu: sync.Mutex{}, cache: nil},
		titles:           titleCache{mu: sync.Mutex{}, cache: nil, inflight: singleflight.Group{}},
		panes:            singleflight.Group{},
		initialReads:     nil,
		workflow:         local.Workflow(),
		actions:          action.Router{Local: local, Peer: remote.Peer, Origin: remote.Origin},
		local:            local,
		remote:           remote,
		viewsMu:          sync.Mutex{},
		deferredViews:    nil,
	}, nil
}

func (s *services) Close() error {
	var readsErr error
	if s.initialReads != nil {
		readsErr = s.initialReads.finish()
	}

	repositoryErr := s.repository.Close()
	if repositoryErr != nil {
		repositoryErr = oops.In("cli").With("operation", "close_repository").Wrap(repositoryErr)
	}

	return errors.Join(readsErr, repositoryErr)
}

func (s *services) startInitialReads(ctx context.Context) {
	if s.currentSessionID == "" {
		return
	}

	s.initialReads = openInitialReads(ctx, s.server, s.currentSessionID)
}

func (s *services) FinishInitialLoad() {
	if s.initialReads != nil {
		s.initialReads.release()
	}
}

func (s *services) readTmux(ctx context.Context, query func(*gotmux.Server) error) error {
	if s.initialReads == nil {
		return query(s.server)
	}

	return s.initialReads.read(ctx, s.server, query)
}

func (s *services) TUIServices() tui.Services {
	return tui.Services{
		Catalog:          s,
		Detector:         s,
		Enterer:          s,
		Actions:          s.actions,
		Panes:            s,
		Agents:           s,
		Tasks:            s,
		Work:             s,
		Inbox:            s,
		Evidence:         s,
		AgentWatcher:     s,
		Remote:           s.remote.Catalog,
		RemoteWatcher:    s.remote.Watcher,
		RemoteAttach:     s.remote.Attach,
		OriginBridge:     s.remote.OriginBridge,
		WorkingDirectory: s,
		State:            s,
	}
}

func (s *services) LoadTasks(ctx context.Context) ([]task.Live, error) {
	var live []task.Live

	err := s.readTmux(ctx, func(server *gotmux.Server) error {
		var inventoryErr error

		live, inventoryErr = task.Inventory(ctx, server, task.NewStore(s.workspace.Tasks))

		return inventoryErr
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_tasks").Wrap(err)
	}

	return live, nil
}

func (s *services) LoadWork(ctx context.Context) ([]work.Item, error) {
	state, err := work.NewStore(s.workspace.Work).Load(ctx)
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_work").Wrap(err)
	}

	return state.Items, nil
}

func (s *services) SyncWork(ctx context.Context) (bool, error) {
	changed, err := work.Sync(ctx, work.NewStore(s.workspace.Work))
	if err != nil {
		return changed, oops.In("cli").With("operation", "sync_work").Wrap(err)
	}

	return changed, nil
}

func (s *services) LoadInbox(ctx context.Context) (inbox.State, error) {
	state, err := inbox.NewStore(s.workspace.Inbox).Load(ctx)
	if err != nil {
		return state, oops.In("cli").With("operation", "load_inbox").Wrap(err)
	}

	return state, nil
}

func (s *services) UpdateInbox(ctx context.Context, change func(*inbox.State) error) (inbox.State, error) {
	state, err := inbox.NewStore(s.workspace.Inbox).Update(ctx, change)
	if err != nil {
		return state, oops.In("cli").With("operation", "update_inbox").Wrap(err)
	}

	return state, nil
}

func (s *services) LastReport(ctx context.Context, agent session.AgentSession) (string, error) {
	// A harness writes the transcript with the conversation's first message, so none means no
	// report yet.
	if _, err := os.Stat(agent.SessionPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	catalog := history.Catalog{
		Sources: []history.Source{{Harness: registry.Harness(agent.Harness), Path: agent.SessionPath}}, IndexPath: "", Progress: nil,
	}

	message, _, err := catalog.LastMessage(ctx, agent.SessionID, "assistant")
	if err != nil {
		return "", oops.In("cli").With("operation", "read_last_report").Wrap(err)
	}

	return message.Text, nil
}

func (s *services) SyncTasks(ctx context.Context, titles map[string]string) (bool, error) {
	changed, err := s.local.SyncTasks(ctx, titles)
	if err != nil {
		return changed, oops.In("cli").With("operation", "sync_tasks").Wrap(err)
	}

	return changed, nil
}

func (s *services) LoadCatalog(ctx context.Context) (session.CatalogResult, error) {
	if s.workspace.Repository == "" {
		return s.listCatalog(ctx, nil)
	}

	// The first frame must not wait on a concurrent save's commit or on the durable open's fsync.
	view, err := store.OpenView(ctx, s.workspace.Repository)
	if err != nil {
		return s.listCatalog(ctx, unreadableSnapshots{err: err})
	}

	result, err := s.listCatalog(ctx, view)
	if closeErr := view.Close(); closeErr != nil {
		err = errors.Join(err, oops.In("cli").With("operation", "close_repository_view").Wrap(closeErr))
	}

	return result, err
}

func (s *services) listCatalog(ctx context.Context, saved session.SavedSnapshots) (session.CatalogResult, error) {
	var result session.CatalogResult

	err := s.readTmux(ctx, func(server *gotmux.Server) error {
		var listErr error

		result, listErr = session.ListCatalog(ctx, session.CatalogOptions{
			Server:           server,
			Repository:       saved,
			RepositorySource: s.workspace.Repository,
			GenerationID:     "",
			CurrentPane:      s.currentPane,
			CurrentSessionID: s.currentSessionID,
		})

		return listErr
	})
	if err != nil {
		// Partial results keep live sessions visible when the saved repository cannot be read.
		return result, oops.In("cli").With("operation", "load_picker_catalog").Wrap(err)
	}

	return result, nil
}

type unreadableSnapshots struct {
	err error
}

func (u unreadableSnapshots) ReadCurrentListing(context.Context) (store.Listing, error) {
	return store.Listing{}, u.err
}

func (u unreadableSnapshots) ReadListing(context.Context, string) (store.Listing, error) {
	return store.Listing{}, u.err
}

func (s *services) TmuxIsInsideTmux() bool {
	return s.popupChild || strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func (s *services) EnterSession(ctx context.Context, target session.Session) (tui.EnterOutcome, error) {
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return enterOutcome(target.Name, tui.EnterCompleted, false), err
	}

	outcome := enterOutcome(target.Name, deferredEnterAction(s.TmuxIsInsideTmux()), false)
	outcome.Target = clonePickerTarget(target)

	return outcome, nil
}

func enterOutcome(name string, enterAction tui.EnterAction, restored bool) tui.EnterOutcome {
	var target session.Session

	return tui.EnterOutcome{SessionName: name, Action: enterAction, Restored: restored, Target: target}
}

func deferredEnterAction(insideTmux bool) tui.EnterAction {
	if insideTmux {
		return tui.EnterNeedsSwitch
	}

	return tui.EnterNeedsAttach
}

func revalidatePickerLiveTarget(ctx context.Context, server *gotmux.Server, target session.Session) error {
	if target.Kind != session.KindLive || target.Origin != session.OriginLocal || target.ID == "" {
		return errPickerTargetChanged
	}

	rows, err := session.ListLive(ctx, server, "")
	if err != nil {
		return oops.In("cli").With("operation", "revalidate_picker_session").Wrap(err)
	}

	for _, row := range rows {
		if row.ID == target.ID && row.RuntimeKey() == target.RuntimeKey() {
			return nil
		}
	}

	return errPickerTargetChanged
}

func findPickerLiveTarget(ctx context.Context, server *gotmux.Server, id, name string) (session.Session, error) {
	rows, err := session.ListLive(ctx, server, "")
	if err != nil {
		return session.Session{}, oops.In("cli").With("operation", "resolve_picker_session").Wrap(err)
	}

	for _, row := range rows {
		if (id == "" || row.ID == id) && row.Name == name {
			return clonePickerTarget(row), nil
		}
	}

	return session.Session{}, errPickerTargetChanged
}

func (s *services) TmuxActivatePane(ctx context.Context, target session.Session, paneID string) error {
	return s.activatePane(ctx, target, paneID, "")
}

func (s *services) activatePane(ctx context.Context, target session.Session, paneID, clientName string) error {
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	current := agents.CurrentTarget{PaneID: "", Client: clientName, SessionID: "", Session: ""}
	if clientName == "" {
		current.PaneID = s.currentPane
		if current.PaneID == "" {
			current.PaneID = os.Getenv("TMUX_PANE")
		}
	}

	navigator, err := agents.NewNavigator(agents.NavigatorOptions{
		Server:     s.server,
		StatePath:  "",
		Target:     current,
		Controller: nil,
		Source:     s.agentStore,
	})
	if err != nil {
		return oops.In("cli").With("operation", "construct_picker_navigator").Wrap(err)
	}

	_, err = navigator.Focus(ctx, agents.FocusTarget{PaneID: paneID, SessionID: target.ID, SessionName: target.Name})
	if err != nil {
		return oops.In("cli").With("operation", "focus_picker_pane").Wrap(err)
	}

	return nil
}

func (s *services) TmuxPaneLocations(ctx context.Context) ([]tui.PaneLocation, error) {
	value, err, _ := s.panes.Do("panes", func() (any, error) {
		return s.observePaneLocations(ctx)
	})
	if err != nil {
		return nil, err
	}

	locations, _ := value.([]tui.PaneLocation)

	return locations, nil
}

func (s *services) observePaneLocations(ctx context.Context) ([]tui.PaneLocation, error) {
	var panes []agents.PaneLocation

	err := s.readTmux(ctx, func(server *gotmux.Server) error {
		var observeErr error

		panes, observeErr = agents.ObservePanes(ctx, server)

		return observeErr
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "observe_picker_panes").Wrap(err)
	}

	result := make([]tui.PaneLocation, 0, len(panes))
	for _, pane := range panes {
		result = append(result, tui.PaneLocation{
			ServerSocket: pane.ServerSocket,
			SessionID:    pane.SessionID,
			SessionName:  pane.SessionName,
			WindowID:     pane.WindowID,
			WindowIndex:  pane.WindowIndex,
			PaneID:       pane.PaneID,
			PaneIndex:    pane.PaneIndex,
			PanePID:      pane.PanePID,
			Command:      pane.Command,
			PaneLeft:     strconv.Itoa(pane.Left),
			PaneTop:      strconv.Itoa(pane.Top),
		})
	}

	return result, nil
}

func (s *services) LoadAgentSummaries(ctx context.Context) (tui.AgentSnapshot, error) {
	observed, err := agents.LoadLiveAgents(ctx, s.agentStore)
	if err != nil {
		return tui.AgentSnapshot{}, oops.In("cli").With("operation", "load_picker_agents").Wrap(err)
	}

	return s.agentSnapshot(ctx, observed), nil
}

func (s *services) WatchAgentSummaries(
	ctx context.Context,
	publish func(revision uint64, snapshot tui.AgentSnapshot, err error) error,
) error {
	var filter aht.Filter

	err := s.agentStore.Watch(ctx, filter, func(snapshot aht.StateSnapshot) error {
		return publish(snapshot.Revision, s.agentSnapshot(ctx, agents.ObservedAgentSessions(snapshot.Sessions)), nil)
	})
	if err != nil {
		return oops.In("cli").With("operation", "watch_picker_agents").Wrap(err)
	}

	return nil
}

func (s *services) agentSnapshot(ctx context.Context, observed []agents.AgentSession) tui.AgentSnapshot {
	var (
		panes    []tui.PaneLocation
		panesErr error
		observer sync.WaitGroup
	)

	if len(observed) > 0 {
		observer.Go(func() {
			paneCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			panes, panesErr = s.TmuxPaneLocations(paneCtx)
		})
	}

	summaries := agentSummaries(observed)
	s.sessionNames.annotateInventory(ctx, summaries)
	s.titles.annotate(ctx, observed, summaries)

	observer.Wait()

	return tui.AgentSnapshot{Summaries: summaries, Panes: panes, PanesObserved: len(observed) > 0 && panesErr == nil}
}

func agentSummaries(observed []agents.AgentSession) []session.AgentSummary {
	type key struct{ socket, id, name string }

	index := make(map[key]int)
	result := make([]session.AgentSummary, 0)

	for _, item := range observed {
		group := key{socket: item.ServerSocket, id: item.SessionID, name: item.SessionName}

		position, ok := index[group]
		if !ok {
			position = len(result)
			index[group] = position

			result = append(result, session.AgentSummary{
				TmuxServerSocket:  item.ServerSocket,
				TmuxSessionID:     item.SessionID,
				TmuxSessionName:   item.SessionName,
				Total:             0,
				Active:            0,
				Waiting:           0,
				Failed:            0,
				Interrupted:       0,
				Unknown:           0,
				IdleSince:         time.Time{},
				WaitingSince:      time.Time{},
				ActivityChangedAt: time.Time{},
				Agents:            nil,
			})
		}

		summary := &result[position]
		agent := agentSession(item)
		summary.Agents = append(summary.Agents, agent)
		summary.Total++
		summary.ActivityChangedAt = laterTime(summary.ActivityChangedAt, item.ActivityChanged)

		switch agent.Status {
		case session.AgentStatusRunning:
			summary.Active++
		case session.AgentStatusWaiting:
			summary.Waiting++
			summary.WaitingSince = earlierNonZero(summary.WaitingSince, agent.StatusSince)
		case session.AgentStatusFailed:
			summary.Failed++
		case session.AgentStatusInterrupted:
			summary.Interrupted++
			summary.IdleSince = earlierNonZero(summary.IdleSince, agent.StatusSince)
		case session.AgentStatusIdle:
			summary.IdleSince = earlierNonZero(summary.IdleSince, agent.StatusSince)
		case session.AgentStatusUnknown, session.AgentStatusSleeping, session.AgentStatusMerged, session.AgentStatusClosed:
			summary.Unknown++
		}
	}

	return result
}

func agentSession(item agents.AgentSession) session.AgentSession {
	detail := session.AgentSession{
		PositionKnown:    false,
		WindowIndex:      0,
		PaneLeft:         0,
		PaneTop:          0,
		RegistryID:       item.ID,
		Harness:          item.Harness,
		HarnessSession:   item.HarnessSessionID,
		SessionID:        item.HarnessSessionID,
		Host:             "",
		PaneID:           item.PaneID,
		PanePID:          item.PanePID,
		CWD:              item.CWD,
		ProjectRoot:      item.ProjectRoot,
		GitBranch:        "",
		Worktree:         "",
		GitRepo:          "",
		GitHasRemote:     false,
		GitChanges:       "",
		LinkedWorktree:   false,
		GitAhead:         0,
		GitBehind:        0,
		GitTrackingKnown: false,
		GitStash:         0,
		GitConflicts:     0,
		GitDirty:         false,
		GitUnmerged:      0,
		SessionPath:      item.SessionPath,
		Executable:       item.Command,
		PID:              item.PanePID,
		Status:           agentStatus(item),
		InitialIdle:      item.InitialIdle,
		StatusSince:      item.ActivityChanged,
		Model:            item.Model,
		WaitingReason:    item.WaitingReason,
		FailureReason:    item.FailureReason,
		Detail:           item.Detail,
		LastTool:         item.LastTool,
		Task:             "",
		PromptAt:         item.PromptAt,
	}
	detail.HarnessSession = harnessSessionLabel(detail)

	return detail
}

func agentStatus(item agents.AgentSession) session.AgentStatus {
	switch item.Attention {
	case string(agents.StateWaiting):
		return session.AgentStatusWaiting
	case string(agents.StateFailed):
		return session.AgentStatusFailed
	case "idle":
		if item.Activity == string(aht.ActivityInterrupted) {
			return session.AgentStatusInterrupted
		}

		return session.AgentStatusIdle
	}

	switch item.Activity {
	case string(aht.ActivityRunning):
		return session.AgentStatusRunning
	case string(aht.ActivityInterrupted):
		return session.AgentStatusInterrupted
	case string(aht.ActivityIdle):
		return session.AgentStatusIdle
	case string(aht.ActivityWaiting):
		return session.AgentStatusWaiting
	case string(aht.ActivityFailed):
		return session.AgentStatusFailed
	default:
		return session.AgentStatusUnknown
	}
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}

	return left
}

func earlierNonZero(left, right time.Time) time.Time {
	if left.IsZero() || (!right.IsZero() && right.Before(left)) {
		return right
	}

	return left
}

func (*services) GetWorkingDirectory() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", oops.In("cli").With("operation", "picker_working_directory").Wrap(err)
	}

	return directory, nil
}

func (s *services) UpdateModelState(ctx context.Context, update session.StateUpdate) error {
	_, err := session.UpdateState(ctx, s.workspace.History, update)
	if err != nil {
		return oops.In("cli").With("operation", "update_picker_state").Wrap(err)
	}

	return nil
}

func (s *services) DeferAgentViews(views []tui.AgentView) bool {
	if !s.popupChild {
		return false
	}

	// The invoking process records these with its switch, so the popup never waits on a history
	// write.
	s.viewsMu.Lock()
	defer s.viewsMu.Unlock()

	for _, view := range views {
		index := slices.IndexFunc(s.deferredViews, func(existing tui.AgentView) bool {
			return existing.Session == view.Session && existing.PaneID == view.PaneID
		})
		if index >= 0 {
			s.deferredViews = slices.Delete(s.deferredViews, index, index+1)
		}

		s.deferredViews = append(s.deferredViews, view)
	}

	if excess := len(s.deferredViews) - handoffMaxAgentViews; excess > 0 {
		s.deferredViews = slices.Delete(s.deferredViews, 0, excess)
	}

	return true
}

func (s *services) takeDeferredAgentViews() []tui.AgentView {
	s.viewsMu.Lock()
	defer s.viewsMu.Unlock()

	views := s.deferredViews
	s.deferredViews = nil

	return views
}

func clonePickerTarget(target session.Session) session.Session {
	target.NameRaw = slices.Clone(target.NameRaw)
	target.PathRaw = slices.Clone(target.PathRaw)
	target.Agents = slices.Clone(target.Agents)

	return target
}

func (s *services) performDeferredSwitch(ctx context.Context, target session.Session, paneID string) error {
	return s.performSwitch(ctx, target, paneID, "", "")
}

func (s *services) performSwitch(ctx context.Context, target session.Session, paneID, clientName, originSessionID string) error {
	target = clonePickerTarget(target)
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	return s.performSwitchValidated(ctx, target, paneID, clientName, originSessionID, nil)
}

func (s *services) performSwitchValidated(
	ctx context.Context,
	target session.Session,
	paneID, clientName, originSessionID string,
	views []tui.AgentView,
) error {
	var outcome session.EnterOutcome

	var err error
	if clientName != "" && originSessionID != "" {
		outcome, err = s.workflow.SwitchSessionOutcomeForClientTarget(ctx, target, clientName, originSessionID)
	} else {
		outcome, err = s.workflow.SwitchSessionOutcome(ctx, target.Name)
	}

	if err != nil {
		switchErr := oops.In("cli").With("operation", "switch_picker_session").Wrap(err)

		return errors.Join(switchErr, s.recordPickerHistory(ctx, "", views))
	}

	viewPaneID := paneID
	if viewPaneID == "" {
		viewPaneID = target.ActivePaneID
	}

	if viewPaneID != "" {
		views = append(views, tui.AgentView{Session: target.SemanticIdentity(), PaneID: viewPaneID})
	}

	var activateErr error
	if paneID != "" {
		activateErr = s.activatePane(ctx, target, paneID, outcome.Client)
	}

	// History fsyncs, so it is recorded once, after the switch and pane focus.
	return errors.Join(activateErr, s.recordPickerHistory(ctx, outcome.SessionName, views))
}

func (s *services) recordPickerHistory(ctx context.Context, switched string, views []tui.AgentView) error {
	identity, recordSwitch := s.workflow.SwitchHistoryIdentity(switched)
	if s.workspace.History == "" || (!recordSwitch && len(views) == 0) {
		return nil
	}

	_, err := session.UpdateState(ctx, s.workspace.History, func(st *session.State) {
		if recordSwitch {
			st.RecordSwitch(identity)
		}

		for _, view := range views {
			st.RecordAgentView(view.Session, view.PaneID)
		}
	})
	if err != nil {
		return oops.In("cli").Code("history_failed").With("operation", "record_picker_history").Wrap(err)
	}

	return nil
}

func (s *services) performDeferredAttach(ctx context.Context, target session.Session) error {
	target = clonePickerTarget(target)
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	_, err := s.workflow.AttachSession(ctx, target.Name)
	if err != nil {
		return oops.In("cli").With("operation", "attach_picker_session").Wrap(err)
	}

	if target.ActivePaneID != "" && s.workspace.History != "" {
		_, err := session.UpdateState(ctx, s.workspace.History, func(st *session.State) {
			st.RecordAgentView(target.SemanticIdentity(), target.ActivePaneID)
		})
		if err != nil {
			zerolog.Ctx(ctx).Warn().Str("component", "agents").Str("operation", "record_view").
				Str("status", "degraded").Str("reason", "history_update_failed").Func(logging.Cause(err)).
				Msg("agent navigation history was not recorded")
		}
	}

	return nil
}

func (s *services) detachInvokingClient(ctx context.Context) error {
	client, err := invokingPickerClient(ctx, s.server)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_picker_client").Wrap(err)
	}

	if err := client.Handle().Detach(ctx); err != nil {
		return oops.In("cli").With("operation", "detach_picker_client").Wrap(err)
	}

	return nil
}
