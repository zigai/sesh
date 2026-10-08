//go:build linux || darwin

package picker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sync/singleflight"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/tui"
)

var (
	errPickerTargetChanged                               = errors.New("selected session changed since it was observed")
	errPickerPartialSave                                 = errors.New("snapshot capture completed without full parity")
	errPickerPartialRestore                              = errors.New("snapshot restore completed without full parity")
	errPickerRepositoryRequired                          = errors.New("repository is not configured")
	_                           tui.DegradedOutcomeError = (*pickerRestoreOutcomeError)(nil)
)

type RemoteServices struct {
	Catalog      tui.RemoteCatalogSource
	Watcher      tui.RemoteCatalogWatcher
	Actions      tui.RemoteActionService
	Attach       tui.RemoteAttacher
	OriginBridge tui.OriginBridgeService
}

type RemoteFactory func(context.Context, *config.Config, string) (RemoteServices, error)

type pickerServices struct {
	workspace        app.Workspace
	server           *gotmux.Server
	currentPane      string
	currentSessionID string
	popupChild       bool
	repoMu           sync.Mutex
	repository       *store.Repository
	agentStore       agents.SnapshotSource
	sessionNames     pickerTranscriptNameCache
	titles           pickerTitleCache
	panes            singleflight.Group
	initialReads     *pickerInitialReads
	workflow         *session.Workflow
	remote           RemoteServices
	viewsMu          sync.Mutex
	deferredViews    []tui.AgentView
}

type pickerRestoreOutcomeError struct {
	result engine.RestoreResult
	cause  error
}

func AgentSummaries(observed []agents.AgentSession) []session.AgentSummary {
	return pickerAgentSummaries(observed)
}

func (e *pickerRestoreOutcomeError) Error() string {
	details := make([]string, 0, len(e.result.MissingRequirements))
	for _, requirement := range e.result.MissingRequirements {
		details = append(details, requirement.Domain+"/"+requirement.Status+"/"+string(requirement.Reason))
	}

	message := fmt.Sprintf(
		"restore %s; operation %s; reason %s",
		e.result.Status,
		e.result.OperationID,
		e.result.Reason,
	)
	if len(details) != 0 {
		message += "; missing " + strings.Join(details, ",")
	}

	if e.cause != nil {
		message += ": " + e.cause.Error()
	}

	return message
}

func (e *pickerRestoreOutcomeError) Unwrap() error { return e.cause }

func (*pickerRestoreOutcomeError) PickerOutcomeOwned() bool { return true }

func (e *pickerRestoreOutcomeError) PickerDegradedOutcome() bool {
	return e.result.Status == "partial" || (e.cause == nil && len(e.result.MissingRequirements) != 0)
}

func newPickerRestoreOutcomeError(result engine.RestoreResult, cause error) error {
	if result.OperationID == "" && len(result.MissingRequirements) == 0 {
		return cause
	}

	return &pickerRestoreOutcomeError{result: cloneRestoreResult(result), cause: cause}
}

func cloneRestoreResult(result engine.RestoreResult) engine.RestoreResult {
	result.SessionsRestored = slices.Clone(result.SessionsRestored)
	result.MissingRequirements = slices.Clone(result.MissingRequirements)

	return result
}

func newPickerServices(
	ctx context.Context,
	workspace app.Workspace,
	streams gotmux.Streams,
	remote RemoteServices,
) (*pickerServices, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return nil, err
	}

	workflow := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:             server,
		Repository:         nil,
		LeaseRoot:          workspace.LeaseRoot,
		HistoryPath:        workspace.History,
		Hooks:              workspace.Config.Hooks,
		HookRunner:         nil,
		Getenv:             nil,
		Streams:            streams,
		Source:             "picker",
		DeferSwitchHistory: true,
	})

	return &pickerServices{
		workspace:        workspace,
		server:           server,
		currentPane:      "",
		currentSessionID: "",
		popupChild:       false,
		repoMu:           sync.Mutex{},
		repository:       nil,
		agentStore:       workspace.AgentSource(),
		sessionNames:     pickerTranscriptNameCache{mu: sync.Mutex{}, cache: nil},
		titles:           pickerTitleCache{mu: sync.Mutex{}, cache: nil, inflight: singleflight.Group{}},
		panes:            singleflight.Group{},
		initialReads:     nil,
		workflow:         workflow,
		remote:           remote,
		viewsMu:          sync.Mutex{},
		deferredViews:    nil,
	}, nil
}

func (s *pickerServices) Close() error {
	var readsErr error
	if s.initialReads != nil {
		readsErr = s.initialReads.finish()
	}

	return errors.Join(readsErr, s.closeRepository())
}

func (s *pickerServices) startInitialReads(ctx context.Context) {
	if s.currentSessionID == "" {
		return
	}

	s.initialReads = openPickerInitialReads(ctx, s.server, s.currentSessionID)
}

func (s *pickerServices) FinishInitialLoad() {
	if s.initialReads != nil {
		s.initialReads.release()
	}
}

func (s *pickerServices) readTmux(ctx context.Context, query func(*gotmux.Server) error) error {
	if s.initialReads == nil {
		return query(s.server)
	}

	return s.initialReads.read(ctx, s.server, query)
}

func (s *pickerServices) closeRepository() error {
	s.repoMu.Lock()
	defer s.repoMu.Unlock()

	if s == nil || s.repository == nil {
		return nil
	}

	err := s.repository.Close()
	s.repository = nil

	if err != nil {
		return oops.In("cli").With("operation", "close_repository").Wrap(err)
	}

	return nil
}

func (s *pickerServices) getRepository(ctx context.Context) (*store.Repository, error) {
	s.repoMu.Lock()
	defer s.repoMu.Unlock()

	if s.repository != nil {
		return s.repository, nil
	}

	if s.workspace.Repository == "" {
		return nil, errPickerRepositoryRequired
	}

	repo, err := store.Open(ctx, s.workspace.Repository)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_repository").Wrap(err)
	}

	s.repository = repo
	if s.workflow != nil {
		s.workflow.SetRepository(repo)
	}

	return s.repository, nil
}

func (s *pickerServices) TUIServices() tui.Services {
	return tui.Services{
		Catalog:          s,
		Detector:         s,
		Enterer:          s,
		Renamer:          s,
		Killer:           s,
		Deleter:          s,
		Panes:            s,
		TerminalAttach:   nil,
		Agents:           s,
		AgentWatcher:     s,
		Snapshots:        s,
		Remote:           s.remote.Catalog,
		RemoteWatcher:    s.remote.Watcher,
		RemoteActions:    s.remote.Actions,
		RemoteAttach:     s.remote.Attach,
		OriginBridge:     s.remote.OriginBridge,
		WorkingDirectory: s,
		State:            s,
	}
}

// LoadCatalog lists saved sessions through a lock-free repository view: the picker's first
// frame must not wait on a concurrent save's commit or on the durable open's fsync. Actions
// that act on a saved session reopen the repository durably through getRepository.
func (s *pickerServices) LoadCatalog(ctx context.Context) (session.CatalogResult, error) {
	if s.workspace.Repository == "" {
		return s.listCatalog(ctx, nil)
	}

	// An unreadable repository leaves saved rows out.
	view, err := store.OpenView(ctx, s.workspace.Repository)
	if err != nil {
		return s.listCatalog(ctx, nil)
	}

	result, err := s.listCatalog(ctx, view)
	if closeErr := view.Close(); closeErr != nil {
		err = errors.Join(err, oops.In("cli").With("operation", "close_repository_view").Wrap(closeErr))
	}

	return result, err
}

func (s *pickerServices) listCatalog(ctx context.Context, saved session.SavedSnapshots) (session.CatalogResult, error) {
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

func (s *pickerServices) TmuxIsInsideTmux() bool {
	return s.popupChild || strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func (s *pickerServices) EnterSession(ctx context.Context, target session.Session) (tui.EnterOutcome, error) {
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, false), err
	}

	outcome := pickerEnterOutcome(target.Name, pickerDeferredEnterAction(s.TmuxIsInsideTmux()), false)
	outcome.Target = clonePickerTarget(target)

	return outcome, nil
}

func (s *pickerServices) CreateAndEnterSession(ctx context.Context, name, dir string) (tui.EnterOutcome, error) {
	created, err := s.workflow.CreateSession(ctx, name, dir)
	if err != nil {
		return s.enterExistingPickerSession(ctx, name, err)
	}

	live, err := findPickerLiveTarget(ctx, s.server, string(created.ID()), name)
	if err != nil {
		return pickerEnterOutcome(name, tui.EnterCompleted, false), err
	}

	outcome := pickerEnterOutcome(name, pickerDeferredEnterAction(s.TmuxIsInsideTmux()), false)
	outcome.Target = live

	return outcome, nil
}

func (s *pickerServices) RestoreAndEnterSession(ctx context.Context, target session.Session) (tui.EnterOutcome, error) {
	if target.Kind != session.KindSaved || target.Origin != session.OriginLocal || target.ID == "" || target.Generation == "" {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, false), errPickerTargetChanged
	}

	repo, err := s.getRepository(ctx)
	if err != nil {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, false), err
	}

	result, err := engine.Restore(ctx, repo, s.server, engine.RestoreConfig{
		LeaseRoot:              s.workspace.LeaseRoot,
		Generation:             target.Generation,
		Sessions:               []string{target.ID},
		OperationID:            "",
		LaunchProcesses:        true,
		AllowMissingWorkingDir: false,
		OnCollision:            engine.CollisionPolicyDefault,
		Profile:                engine.RecoveryPractical,
	})
	if err != nil {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, false), newPickerRestoreOutcomeError(result, err)
	}

	if (result.Status != "complete" && result.Status != "best_effort") || !result.Verified || result.Uncertain {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, false), newPickerRestoreOutcomeError(result, errPickerPartialRestore)
	}

	live, err := findPickerLiveTarget(ctx, s.server, "", target.Name)
	if err != nil {
		return pickerEnterOutcome(target.Name, tui.EnterCompleted, true), err
	}

	outcome := pickerEnterOutcome(target.Name, pickerDeferredEnterAction(s.TmuxIsInsideTmux()), true)
	outcome.Target = live

	return outcome, nil
}

func pickerEnterOutcome(name string, action tui.EnterAction, restored bool) tui.EnterOutcome {
	var target session.Session

	return tui.EnterOutcome{SessionName: name, Action: action, Restored: restored, Target: target}
}

func pickerDeferredEnterAction(insideTmux bool) tui.EnterAction {
	if insideTmux {
		return tui.EnterNeedsSwitch
	}

	return tui.EnterNeedsAttach
}

func (s *pickerServices) RenameSession(ctx context.Context, target session.Session, newName string) error {
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	if err := s.workflow.RenameSessionTarget(ctx, target.Name, newName, target.ID); err != nil {
		return oops.In("cli").With("operation", "rename_picker_session").Wrap(err)
	}

	return nil
}

func (s *pickerServices) KillSession(ctx context.Context, target session.Session) error {
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	if err := s.workflow.KillSessionTarget(ctx, target.Name, target.ID, false); err != nil {
		return oops.In("cli").With("operation", "kill_picker_session").Wrap(err)
	}

	return nil
}

func (s *pickerServices) DeleteSessionEverywhere(ctx context.Context, target session.Session, force bool) (tui.DeleteResult, error) {
	if target.Kind == session.KindLive {
		if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
			return tui.DeleteResult{}, err
		}
	}

	expectedID := ""
	if target.Kind == session.KindLive {
		expectedID = target.ID
	}

	_, _ = s.getRepository(ctx)

	outcome, err := s.workflow.DeleteSessionTargetEverywhere(ctx, target.Name, expectedID, force)

	result := tui.DeleteResult{LiveKilled: outcome.LiveKilled, SavedDeleted: outcome.SavedDeleted}
	if err != nil {
		return result, oops.In("cli").With("operation", "delete_picker_session").Wrap(err)
	}

	return result, nil
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

func (s *pickerServices) TmuxActivatePane(ctx context.Context, target session.Session, paneID string) error {
	return s.activatePane(ctx, target, paneID, "")
}

func (s *pickerServices) activatePane(ctx context.Context, target session.Session, paneID, clientName string) error {
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

func (s *pickerServices) TmuxPaneLocations(ctx context.Context) ([]tui.PaneLocation, error) {
	value, err, _ := s.panes.Do("panes", func() (any, error) {
		return s.observePaneLocations(ctx)
	})
	if err != nil {
		return nil, err
	}

	locations, _ := value.([]tui.PaneLocation)

	return locations, nil
}

func (s *pickerServices) observePaneLocations(ctx context.Context) ([]tui.PaneLocation, error) {
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

func (s *pickerServices) SnapshotSession(ctx context.Context, target session.Session) error {
	processEnv, err := s.workspace.ProcessEnvironment()
	if err != nil {
		return oops.In("cli").With("operation", "snapshot_picker_session").Wrap(err)
	}

	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	repo, err := s.getRepository(ctx)
	if err != nil {
		return err
	}

	result, err := engine.SaveObservation(ctx, repo, s.server, []gotmux.SessionID{gotmux.SessionID(target.ID)}, engine.SaveConfig{
		LeaseRoot:              s.workspace.LeaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: processEnv,
		AgentSource:            s.agentStore,
		Profile:                engine.RecoveryPractical,
	})
	if err != nil {
		return oops.In("cli").With("operation", "snapshot_picker_session").Wrap(err)
	}

	if result.Status != "complete" && result.Status != "best_effort" {
		return errPickerPartialSave
	}

	return nil
}

func (s *pickerServices) LoadAgentSummaries(ctx context.Context) (tui.AgentSnapshot, error) {
	observed, err := agents.LoadLiveAgents(ctx, s.agentStore)
	if err != nil {
		return tui.AgentSnapshot{}, oops.In("cli").With("operation", "load_picker_agents").Wrap(err)
	}

	return s.agentSnapshot(ctx, observed), nil
}

func (s *pickerServices) WatchAgentSummaries(
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

func (s *pickerServices) agentSnapshot(ctx context.Context, observed []agents.AgentSession) tui.AgentSnapshot {
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

	summaries := pickerAgentSummaries(observed)
	s.sessionNames.annotateInventory(ctx, summaries)
	s.titles.annotate(ctx, observed, summaries)

	observer.Wait()

	return tui.AgentSnapshot{Summaries: summaries, Panes: panes, PanesObserved: len(observed) > 0 && panesErr == nil}
}

func pickerAgentSummaries(observed []agents.AgentSession) []session.AgentSummary {
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
		agent := pickerAgentSession(item)
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
		case session.AgentStatusUnknown:
			summary.Unknown++
		}
	}

	return result
}

func pickerAgentSession(item agents.AgentSession) session.AgentSession {
	detail := session.AgentSession{
		PositionKnown:    false,
		WindowIndex:      0,
		PaneLeft:         0,
		PaneTop:          0,
		RegistryID:       item.ID,
		Harness:          item.Harness,
		HarnessSession:   item.HarnessSessionID,
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
		SessionPath:      item.SessionPath,
		Executable:       item.Command,
		PID:              item.PanePID,
		Status:           pickerAgentStatus(item),
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
	detail.HarnessSession = pickerHarnessSessionLabel(detail)

	return detail
}

func pickerAgentStatus(item agents.AgentSession) session.AgentStatus {
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

func (*pickerServices) GetWorkingDirectory() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", oops.In("cli").With("operation", "picker_working_directory").Wrap(err)
	}

	return directory, nil
}

func (s *pickerServices) UpdateModelState(ctx context.Context, update session.StateUpdate) error {
	_, err := session.UpdateState(ctx, s.workspace.History, update)
	if err != nil {
		return oops.In("cli").With("operation", "update_picker_state").Wrap(err)
	}

	return nil
}

// DeferAgentViews keeps a popup's agent views in memory: the invoking process records them
// with its switch after the popup closes, so the popup never waits on a history write.
func (s *pickerServices) DeferAgentViews(views []tui.AgentView) bool {
	if !s.popupChild {
		return false
	}

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

	if excess := len(s.deferredViews) - pickerHandoffMaxAgentViews; excess > 0 {
		s.deferredViews = slices.Delete(s.deferredViews, 0, excess)
	}

	return true
}

func (s *pickerServices) takeDeferredAgentViews() []tui.AgentView {
	s.viewsMu.Lock()
	defer s.viewsMu.Unlock()

	views := s.deferredViews
	s.deferredViews = nil

	return views
}

func (s *pickerServices) enterExistingPickerSession(ctx context.Context, name string, createErr error) (tui.EnterOutcome, error) {
	if !errors.Is(createErr, session.ErrSessionAlreadyExists) {
		return pickerEnterOutcome(name, tui.EnterCompleted, false), createErr
	}

	target, err := s.server.FindSession(ctx, name)
	if err != nil {
		return pickerEnterOutcome(name, tui.EnterCompleted, false), errors.Join(createErr, err)
	}

	live, err := findPickerLiveTarget(ctx, s.server, string(target.ID()), name)
	if err != nil {
		return pickerEnterOutcome(name, tui.EnterCompleted, false), err
	}

	outcome := pickerEnterOutcome(name, pickerDeferredEnterAction(s.TmuxIsInsideTmux()), false)
	outcome.Target = live

	return outcome, nil
}

func clonePickerTarget(target session.Session) session.Session {
	target.NameRaw = slices.Clone(target.NameRaw)
	target.PathRaw = slices.Clone(target.PathRaw)
	target.Agents = slices.Clone(target.Agents)

	return target
}

func (s *pickerServices) performDeferredSwitch(ctx context.Context, target session.Session, paneID string) error {
	return s.performSwitch(ctx, target, paneID, "", "")
}

func (s *pickerServices) performSwitch(ctx context.Context, target session.Session, paneID, clientName, originSessionID string) error {
	target = clonePickerTarget(target)
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	return s.performSwitchValidated(ctx, target, paneID, clientName, originSessionID, nil)
}

// performSwitchValidated switches to a target the handoff already validated, skipping a second
// catalog pass. History, which fsyncs, is recorded once after the switch and pane focus.
func (s *pickerServices) performSwitchValidated(
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

	return errors.Join(activateErr, s.recordPickerHistory(ctx, outcome.SessionName, views))
}

// recordPickerHistory records a completed switch to switched, when set, and the agent views
// in one history update.
func (s *pickerServices) recordPickerHistory(ctx context.Context, switched string, views []tui.AgentView) error {
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

func (s *pickerServices) performDeferredAttach(ctx context.Context, target session.Session) error {
	target = clonePickerTarget(target)
	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	_, err := s.workflow.AttachSession(ctx, target.Name)
	if err != nil {
		return oops.In("cli").With("operation", "attach_picker_session").Wrap(err)
	}

	if target.ActivePaneID != "" && s.workspace.History != "" {
		_, _ = session.UpdateState(ctx, s.workspace.History, func(st *session.State) {
			st.RecordAgentView(target.SemanticIdentity(), target.ActivePaneID)
		})
	}

	return nil
}

func (s *pickerServices) detachInvokingClient(ctx context.Context) error {
	client, err := invokingPickerClient(ctx, s.server)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_picker_client").Wrap(err)
	}

	if err := client.Handle().Detach(ctx); err != nil {
		return oops.In("cli").With("operation", "detach_picker_client").Wrap(err)
	}

	return nil
}
