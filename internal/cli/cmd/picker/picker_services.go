//go:build linux || darwin

package cmdpicker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
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

type PickerRemoteServices struct {
	Catalog      tui.RemoteCatalogSource
	Watcher      tui.RemoteCatalogWatcher
	Actions      tui.RemoteActionService
	Attach       tui.RemoteAttacher
	OriginBridge tui.OriginBridgeService
}

type PickerRemoteFactory func(context.Context, *config.Config, string) (PickerRemoteServices, error)

type pickerServices struct {
	cfg              *config.Config
	server           *gotmux.Server
	currentPane      string
	currentSessionID string
	popupChild       bool
	repoMu           sync.Mutex
	repository       *store.Repository
	repositoryPath   string
	leaseRoot        string
	historyPath      string
	agentStore       agents.SnapshotSource
	sessionNames     pickerTranscriptNameCache
	workflow         *session.Workflow
	remote           PickerRemoteServices
}

type pickerRestoreOutcomeError struct {
	result engine.RestoreResult
	cause  error
}

func PickerAgentSummaries(observed []agents.AgentSession) []session.AgentSummary {
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
	cfg *config.Config,
	socketPath, repositoryPath, leaseRoot, historyPath, agentStatePath string,
	streams gotmux.TerminalStreams,
	remote PickerRemoteServices,
) (*pickerServices, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	workflow := session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server:      server,
		Repository:  nil,
		LeaseRoot:   leaseRoot,
		HistoryPath: historyPath,
		Hooks:       cfg.Hooks,
		HookRunner:  nil,
		Getenv:      nil,
		Streams:     streams,
		Source:      "picker",
	})

	resolvedAgentStatePath := agentStatePath
	if resolvedAgentStatePath == "" && cfg != nil && cfg.AgentStatus.StorePath != "" {
		resolvedAgentStatePath = cfg.AgentStatus.StorePath
	}

	return &pickerServices{
		cfg:              cfg,
		server:           server,
		currentPane:      "",
		currentSessionID: "",
		popupChild:       false,
		repoMu:           sync.Mutex{},
		repository:       nil,
		repositoryPath:   repositoryPath,
		leaseRoot:        leaseRoot,
		historyPath:      historyPath,
		agentStore:       agents.NewAHTStore(resolvedAgentStatePath),
		sessionNames:     pickerTranscriptNameCache{mu: sync.Mutex{}, cache: nil},
		workflow:         workflow,
		remote:           remote,
	}, nil
}

func (s *pickerServices) Close() error {
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

	if s.repositoryPath == "" {
		return nil, errPickerRepositoryRequired
	}

	repo, err := store.Open(ctx, s.repositoryPath)
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

func (s *pickerServices) LoadCatalog(ctx context.Context) (session.CatalogResult, error) {
	repo, _ := s.getRepository(ctx)

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server:           s.server,
		Repository:       repo,
		RepositorySource: s.repositoryPath,
		GenerationID:     "",
		CurrentPane:      s.currentPane,
		CurrentSessionID: s.currentSessionID,
	})
	if err != nil {
		return session.CatalogResult{}, oops.In("cli").With("operation", "load_picker_catalog").Wrap(err)
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
		LeaseRoot:              s.leaseRoot,
		Generation:             target.Generation,
		Sessions:               []string{target.ID},
		OperationID:            "",
		LaunchProcesses:        true,
		AllowMissingWorkingDir: false,
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
	panes, err := agents.ObservePanes(ctx, s.server)
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
	var processEnv []string
	if s.cfg != nil {
		processEnv = s.cfg.Snapshot.ProcessEnvironment
	} else {
		processEnv = config.DefaultProcessEnvironment
	}

	if err := process.ValidateEnvironmentKeys(processEnv); err != nil {
		return oops.In("cli").With("operation", "snapshot_picker_session").Wrap(err)
	}

	processEnv = config.FilterProcessEnvironmentKeys(processEnv)

	if err := revalidatePickerLiveTarget(ctx, s.server, target); err != nil {
		return err
	}

	repo, err := s.getRepository(ctx)
	if err != nil {
		return err
	}

	result, err := engine.SaveObservation(ctx, repo, s.server, []gotmux.SessionID{gotmux.SessionID(target.ID)}, engine.SaveConfig{
		LeaseRoot:              s.leaseRoot,
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

func (s *pickerServices) LoadAgentSummaries(ctx context.Context) ([]session.AgentSummary, error) {
	observed, err := agents.LoadLiveAgents(ctx, s.agentStore)
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_picker_agents").Wrap(err)
	}

	summaries := pickerAgentSummaries(observed)
	s.sessionNames.annotateInventory(ctx, summaries)
	annotatePickerNativeTitles(ctx, observed, summaries)

	return summaries, nil
}

func (s *pickerServices) WatchAgentSummaries(
	ctx context.Context,
	publish func(revision uint64, summaries []session.AgentSummary, err error) error,
) error {
	var filter aht.Filter

	err := s.agentStore.Watch(ctx, filter, func(snapshot aht.StateSnapshot) error {
		observed := agents.ObservedAgentSessions(snapshot.Sessions)
		summaries := pickerAgentSummaries(observed)
		s.sessionNames.annotateInventory(ctx, summaries)
		annotatePickerNativeTitles(ctx, observed, summaries)

		return publish(snapshot.Revision, summaries, nil)
	})
	if err != nil {
		return oops.In("cli").With("operation", "watch_picker_agents").Wrap(err)
	}

	return nil
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
		GitChanges:       "",
		LinkedWorktree:   false,
		GitAhead:         0,
		GitBehind:        0,
		GitTrackingKnown: false,
		SessionPath:      item.SessionPath,
		Executable:       item.Command,
		PID:              item.PanePID,
		Status:           pickerAgentStatus(item),
		StatusSince:      item.ActivityChanged,
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
	_, err := session.UpdateState(ctx, s.historyPath, update)
	if err != nil {
		return oops.In("cli").With("operation", "update_picker_state").Wrap(err)
	}

	return nil
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

func pickerPaths(cfg *config.Config, repository, leaseRoot, history string) (string, string, string, error) {
	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return "", "", "", oops.In("cli").With("operation", "resolve_picker_data_directory").Wrap(err)
	}

	resolve := func(value, fallback string) (string, error) {
		if value == "" {
			value = fallback
		}

		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_picker_path").Wrap(err)
		}

		return absolute, nil
	}

	repository, err = resolve(repository, filepath.Join(dataDir, "snapshots"))
	if err != nil {
		return "", "", "", err
	}

	leaseRoot, err = resolve(leaseRoot, filepath.Join(dataDir, "leases"))
	if err != nil {
		return "", "", "", err
	}

	if history == "" {
		history = filepath.Join(config.GetXDGPaths().StateHome, "sesh", "history.json")
	}

	history, err = resolve(history, history)
	if err != nil {
		return "", "", "", err
	}

	return repository, leaseRoot, history, nil
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

	return s.performSwitchValidated(ctx, target, paneID, clientName, originSessionID)
}

// performSwitchValidated skips the duplicate catalog pass after a fresh handoff lookup.
func (s *pickerServices) performSwitchValidated(ctx context.Context, target session.Session, paneID, clientName, originSessionID string) error {
	var outcome session.EnterOutcome

	var err error
	if clientName != "" && originSessionID != "" {
		outcome, err = s.workflow.SwitchSessionOutcomeForClientTarget(ctx, target, clientName, originSessionID)
	} else {
		outcome, err = s.workflow.SwitchSessionOutcome(ctx, target.Name)
	}

	if err != nil {
		return oops.In("cli").With("operation", "switch_picker_session").Wrap(err)
	}

	viewPaneID := paneID
	if viewPaneID == "" {
		viewPaneID = target.ActivePaneID
	}

	if viewPaneID != "" && s.historyPath != "" {
		_, _ = session.UpdateState(ctx, s.historyPath, func(st *session.State) {
			st.RecordAgentView(target.SemanticIdentity(), viewPaneID)
		})
	}

	if paneID == "" {
		return nil
	}

	return s.activatePane(ctx, target, paneID, outcome.Client)
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

	if target.ActivePaneID != "" && s.historyPath != "" {
		_, _ = session.UpdateState(ctx, s.historyPath, func(st *session.State) {
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
