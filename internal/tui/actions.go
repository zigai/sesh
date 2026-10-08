package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
)

var (
	errSavedSessionAlreadyLive  = errors.New("saved session is already live")
	errSelectedAgentPaneMissing = errors.New("selected agent has no live tmux pane")
	errPaneServiceUnavailable   = errors.New("pane service unavailable")
	errRemoteActionUnsupported  = errors.New("remote sessions are read-only in this build")
)

func (m *Model) runLiveSessionAction(target session.Session) tea.Cmd {
	if m.mode == modePopup {
		return func() tea.Msg {
			return sessionActionResultMsg{
				operation:      tuiOperationEnterLiveSession,
				deferred:       true,
				attach:         false,
				err:            nil,
				sessionName:    target.Name,
				target:         target,
				switchSession:  target.Name,
				switchPane:     "",
				quit:           true,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}
	}

	var agentView *agentViewStateUpdate
	if target.ActivePaneID != "" {
		agentView = &agentViewStateUpdate{identity: target.SemanticIdentity(), paneID: target.ActivePaneID}
	}

	return m.sessionActionCommandWithAgentView(tuiOperationEnterLiveSession, target, agentView, func(ctx context.Context) (EnterOutcome, error) {
		if m.deps.Enterer == nil {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false, Target: target}, errEnterUnavailable
		}

		outcome, err := m.deps.Enterer.EnterSession(ctx, target)
		if err != nil {
			return outcome, oops.In("tui").With("operation", "enter_live_session", "action", "enter_live").Wrap(err)
		}

		return outcome, nil
	})
}

func (m *Model) runAgentSessionAction(target session.Session, agent session.AgentSession) tea.Cmd {
	sessionName := target.Name

	if agent.PaneID == "" {
		return func() tea.Msg {
			return sessionActionResultMsg{
				operation:      tuiOperationActivateAgentPane,
				deferred:       false,
				attach:         false,
				err:            errSelectedAgentPaneMissing,
				sessionName:    sessionName,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}
	}

	if m.mode == modePopup {
		return func() tea.Msg {
			return sessionActionResultMsg{
				operation:      tuiOperationActivateAgentPane,
				deferred:       true,
				attach:         false,
				err:            nil,
				sessionName:    sessionName,
				target:         target,
				switchSession:  sessionName,
				switchPane:     agent.PaneID,
				quit:           true,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}
	}

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		if m.deps.Panes == nil {
			return sessionActionResultMsg{
				operation:      tuiOperationActivateAgentPane,
				deferred:       false,
				attach:         false,
				err:            errPaneServiceUnavailable,
				sessionName:    sessionName,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}

		if err := m.deps.Panes.TmuxActivatePane(m.operationContext(), target, agent.PaneID); err != nil {
			return sessionActionResultMsg{
				operation:      tuiOperationActivateAgentPane,
				deferred:       false,
				attach:         false,
				err:            err,
				sessionName:    sessionName,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}

		view := &agentViewStateUpdate{identity: target.SemanticIdentity(), paneID: agent.PaneID}
		if err := m.persistSessionState(m.operationContext(), target, view); err != nil {
			return sessionActionResultMsg{
				operation:      tuiOperationActivateAgentPane,
				deferred:       false,
				attach:         false,
				err:            oops.In("tui").With("operation", "save_state", "action", "activate_agent_pane").Wrap(err),
				sessionName:    sessionName,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}

		return sessionActionResultMsg{
			operation:      tuiOperationActivateAgentPane,
			deferred:       false,
			attach:         false,
			err:            nil,
			sessionName:    sessionName,
			target:         target,
			switchSession:  "",
			switchPane:     "",
			quit:           true,
			refresh:        false,
			statePersisted: true,
			agentView:      view,
		}
	}
}

func (m *Model) runSessionActionWithCreateDir(sessionName string, dir string) tea.Cmd {
	var runtimeIdentity session.RuntimeIdentity

	target := session.Session{
		ID:                     "",
		Name:                   sessionName,
		TmuxServerSocket:       "",
		Kind:                   session.KindLive,
		Origin:                 session.OriginLocal,
		Generation:             "",
		RepositorySource:       "",
		NameRaw:                nil,
		PathRaw:                nil,
		RuntimeIdentity:        runtimeIdentity,
		RemoteStatus:           session.RemoteStatusAvailable,
		WindowCount:            0,
		Attached:               false,
		Activity:               time.Time{},
		IsCurrent:              false,
		ActivePaneID:           "",
		ActiveWindowName:       "",
		ActiveWindowIndex:      "",
		ActivePanePath:         dir,
		ActivePaneCommand:      "",
		ActivePaneTitle:        "",
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}

	return m.sessionActionCommand(tuiOperationEnterOrCreateSession, target, func(ctx context.Context) (EnterOutcome, error) {
		if m.deps.Enterer == nil {
			return EnterOutcome{SessionName: sessionName, Action: EnterCompleted, Restored: false, Target: target}, errEnterUnavailable
		}

		return m.deps.Enterer.CreateAndEnterSession(ctx, sessionName, dir)
	})
}

func (m *Model) runSavedSessionAction(target session.Session) tea.Cmd {
	return m.sessionActionCommand(tuiOperationRestoreSavedSession, target, func(ctx context.Context) (EnterOutcome, error) {
		if m.deps.Enterer == nil {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false, Target: target}, errEnterUnavailable
		}

		outcome, err := m.deps.Enterer.RestoreAndEnterSession(ctx, target)
		if errors.Is(err, session.ErrSessionAlreadyExists) {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false, Target: target}, oops.In("tui").Code("session_already_live").With("operation", "restore_saved_session", "action", "restore_saved", "session", target.Name).Wrap(errSavedSessionAlreadyLive)
		}

		if err != nil {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false, Target: target}, oops.In("tui").With("operation", "restore_saved_session").Wrap(err)
		}

		return outcome, nil
	})
}

func (m *Model) sessionActionCommand(
	operation tuiLogOperation,
	target session.Session,
	run func(context.Context) (EnterOutcome, error),
) tea.Cmd {
	return m.sessionActionCommandWithAgentView(operation, target, nil, run)
}

func (m *Model) sessionActionCommandWithAgentView(
	operation tuiLogOperation,
	target session.Session,
	agentView *agentViewStateUpdate,
	run func(context.Context) (EnterOutcome, error),
) tea.Cmd {
	ctx := m.operationContext()
	popupMode := m.mode == modePopup

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		outcome, err := run(ctx)
		if err != nil {
			return sessionActionResultMsg{
				operation:      operation,
				deferred:       false,
				attach:         false,
				err:            err,
				sessionName:    target.Name,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}

		persisted := false

		resolvedTarget := target
		if outcome.Target.ID != "" {
			resolvedTarget = outcome.Target
		}

		if outcome.SessionName != "" && outcome.Action == EnterCompleted {
			if err := m.persistSessionState(ctx, resolvedTarget, agentView); err != nil {
				return sessionActionResultMsg{
					operation:      operation,
					deferred:       false,
					attach:         false,
					err:            err,
					sessionName:    target.Name,
					target:         target,
					switchSession:  "",
					switchPane:     "",
					quit:           false,
					refresh:        false,
					statePersisted: false,
					agentView:      nil,
				}
			}

			persisted = true
		}

		return sessionActionResultMsg{
			operation:      operation,
			deferred:       popupMode && outcome.Action == EnterNeedsSwitch,
			attach:         outcome.Action == EnterNeedsAttach,
			err:            nil,
			sessionName:    outcome.SessionName,
			target:         resolvedTarget,
			switchSession:  enterSwitchSession(outcome),
			switchPane:     "",
			quit:           true,
			refresh:        outcome.Restored,
			statePersisted: persisted,
			agentView:      agentView,
		}
	}
}

func enterSwitchSession(outcome EnterOutcome) string {
	if outcome.Action == EnterNeedsSwitch {
		return outcome.SessionName
	}

	return ""
}

func (m *Model) persistSessionState(ctx context.Context, target session.Session, agentView *agentViewStateUpdate) error {
	identity := target.SemanticIdentity()

	if err := m.updateState(ctx, func(current *session.State) {
		current.RecordSwitchIdentity(identity)

		if agentView != nil {
			current.RecordAgentView(agentView.identity, agentView.paneID)
		}
	}); err != nil {
		return oops.In("tui").With("operation", "save_state").Wrap(err)
	}

	return nil
}

func (m *Model) logSessionActionResult(msg sessionActionResultMsg) {
	if msg.err != nil {
		var owned OutcomeOwnedError
		if errors.As(msg.err, &owned) && owned.PickerOutcomeOwned() {
			return
		}

		logTUIFailure(m.operationContext(), msg.operation, msg.err)

		return
	}

	if msg.deferred {
		switch msg.operation {
		case tuiOperationEnterLiveSession, tuiOperationEnterOrCreateSession, tuiOperationActivateAgentPane:
			return
		case tuiOperationLoadCatalog,
			tuiOperationLoadRemoteCatalog,
			tuiOperationLoadOriginSessions,
			tuiOperationLoadAgentSummaries,
			tuiOperationRestoreSavedSession,
			tuiOperationRenameSession,
			tuiOperationKillSession,
			tuiOperationDeleteSession,
			tuiOperationResolveWorkingDirectory,
			tuiOperationSaveState,
			tuiOperationSnapshotSession,
			tuiOperationAgentGitScan,
			tuiOperationCustomFieldRun:
		}
	}

	logTUIEvent(m.operationContext(), msg.operation, tuiLogOutcomeSuccess)
}

func (m *Model) handleSessionActionResult(msg sessionActionResultMsg) (tea.Model, tea.Cmd) {
	if msg.attach && msg.err == nil {
		return m.handleTerminalAttach(msg)
	}

	m.logSessionActionResult(msg)

	if msg.err != nil {
		var degraded DegradedOutcomeError
		if errors.As(msg.err, &degraded) && degraded.PickerDegradedOutcome() {
			m.statusMessage = fmt.Sprintf("%v", msg.err)
			m.statusLevel = statusDegraded
		} else {
			m.setErrorStatusf("%v", msg.err)
		}

		return m, nil
	}

	m.clearStatus()

	if msg.switchSession != "" {
		m.deferredSwitch = msg.target
		m.hasDeferredSwitch = true
		m.switchAfterQuit = msg.switchSession
		m.paneAfterQuit = msg.switchPane
	} else {
		m.deferredSwitch = emptySession()
		m.hasDeferredSwitch = false
		m.switchAfterQuit = ""
		m.paneAfterQuit = ""
	}

	if msg.quit && (msg.switchSession != "" || msg.switchPane != "") {
		return m, tea.Quit
	}

	if !m.finishSessionActionResult(msg) {
		return m, nil
	}

	if msg.quit {
		return m, tea.Quit
	}

	return m, nil
}

func (m *Model) handleTerminalAttach(msg sessionActionResultMsg) (tea.Model, tea.Cmd) {
	if m.deps.TerminalAttach == nil {
		return m.deferTerminalAttach(msg.target)
	}

	cmd, err := m.deps.TerminalAttach.AttachSession(m.operationContext(), msg.target)
	if err != nil {
		m.setErrorStatusf("Terminal attach failed: %v", err)

		return m, nil
	}

	if cmd == nil {
		return m.deferTerminalAttach(msg.target)
	}

	return m, tea.Exec(cmd, func(err error) tea.Msg {
		return sessionActionResultMsg{
			operation:      msg.operation,
			deferred:       false,
			attach:         false,
			err:            err,
			sessionName:    msg.target.Name,
			target:         msg.target,
			switchSession:  "",
			switchPane:     "",
			quit:           false,
			refresh:        err == nil,
			statePersisted: false,
			agentView:      nil,
		}
	})
}

func (m *Model) deferTerminalAttach(target session.Session) (tea.Model, tea.Cmd) {
	m.deferredAttach = target
	m.hasDeferredAttach = true
	m.clearStatus()

	return m, tea.Quit
}

func (m *Model) finishSessionActionResult(msg sessionActionResultMsg) bool {
	if msg.target.Origin == session.OriginBridge && msg.quit {
		m.originDetachAfterQuit = true
	}

	if msg.refresh && !msg.quit {
		if err := m.loadSessions(); err != nil {
			m.setErrorStatusf("Failed to refresh sessions: %v", err)

			return false
		}
	}

	return true
}

func (m *Model) setErrorStatusf(format string, args ...any) {
	m.statusMessage = fmt.Sprintf(format, args...)
	m.statusLevel = statusError
}

func (m *Model) clearStatus() {
	m.statusMessage = ""
	m.statusLevel = statusNone
	m.statusBadgeHovered = false

	m.applyDegradedStatus()
}

func (m *Model) dismissStatus() {
	m.statusMessage = ""
	m.statusLevel = statusNone
	m.degradedSources = nil
	m.statusBadgeHovered = false
}

func (m *Model) handleEnter() func() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.filtered) {
		return nil
	}

	selectedSession := m.filtered[m.cursor]
	selected := m.selectedListRow()

	if selectedSession.Origin == session.OriginSSH {
		return m.handleRemoteEnter(selectedSession, selected)
	}

	if selectedSession.Origin == session.OriginBridge {
		return m.handleOriginBridgeEnter(selectedSession)
	}

	if selected.isAgent() {
		agent := selectedSession.Agents[selected.agentIndex]

		return func() (tea.Model, tea.Cmd) {
			return m, m.runAgentSessionAction(selectedSession, agent)
		}
	}

	return func() (tea.Model, tea.Cmd) {
		if selectedSession.IsSaved() {
			return m, m.runSavedSessionAction(selectedSession)
		}

		return m, m.runLiveSessionAction(selectedSession)
	}
}

func (m *Model) handleOriginBridgeEnter(
	selectedSession session.Session,
) func() (tea.Model, tea.Cmd) {
	if m.deps.OriginBridge == nil {
		m.setErrorStatusf("Enter is not supported on origin sessions: %v", errOriginBridgeDisabled)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return func() (tea.Model, tea.Cmd) {
		return m, func() tea.Msg {
			if !m.lifecycle.BeginWorker() {
				return nil
			}
			defer m.lifecycle.EndWorker()

			err := m.deps.OriginBridge.RequestOriginSwitch(m.operationContext(), selectedSession)

			return sessionActionResultMsg{
				operation: tuiOperationEnterLiveSession, deferred: true,
				target: selectedSession, sessionName: selectedSession.Name, err: err, quit: err == nil,
				attach: false, switchSession: "", switchPane: "", refresh: false, statePersisted: false, agentView: nil,
			}
		}
	}
}

func (m *Model) handleRemoteEnter(
	target session.Session,
	selectedRow popupListRow,
) func() (tea.Model, tea.Cmd) {
	if target.IsSaved() {
		if m.deps.RemoteActions == nil {
			m.setErrorStatusf("Remote restore is not available: remote action service not configured")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		if !m.deps.RemoteActions.IsActionPermitted(m.config, target, "restore") {
			m.setErrorStatusf("Remote restore is disabled by remote permissions")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		return func() (tea.Model, tea.Cmd) {
			return m, m.runRemoteAction(tuiOperationRestoreSavedSession, target, RemoteActionRequest{
				Action: "restore", NewName: "", Directory: "",
			})
		}
	}

	paneID := ""
	if selectedRow.isAgent() && selectedRow.agentIndex < len(target.Agents) {
		paneID = target.Agents[selectedRow.agentIndex].PaneID
	}

	if m.deps.RemoteAttach != nil {
		return func() (tea.Model, tea.Cmd) {
			m.deferredRemoteAttach = target
			m.remoteAttachPane = paneID
			m.hasDeferredRemoteAttach = true

			return m, tea.Quit
		}
	}

	if m.deps.RemoteAttach == nil {
		return m.rejectRemoteAction("Enter")
	}

	m.deferredRemoteAttach = target
	m.remoteAttachPane = paneID
	m.hasDeferredRemoteAttach = true

	return func() (tea.Model, tea.Cmd) { return m, tea.Quit }
}

func (m *Model) runRemoteAction(operation tuiLogOperation, target session.Session, request RemoteActionRequest) tea.Cmd {
	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		if m.deps.RemoteActions == nil {
			return sessionActionResultMsg{
				operation:      operation,
				deferred:       false,
				attach:         false,
				err:            oops.In("tui").Code("remote_actions_disabled").With("operation", "run_remote_action", "action", request.Action).Wrap(errRemoteActionsDisabled),
				sessionName:    target.Name,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        false,
				statePersisted: false,
				agentView:      nil,
			}
		}

		err := m.deps.RemoteActions.RunRemoteAction(m.operationContext(), m.config, target, request)

		return sessionActionResultMsg{
			operation:      operation,
			deferred:       false,
			attach:         false,
			err:            err,
			sessionName:    target.Name,
			target:         target,
			switchSession:  "",
			switchPane:     "",
			quit:           false,
			refresh:        err == nil,
			statePersisted: false,
			agentView:      nil,
		}
	}
}

func (m *Model) rejectRemoteAction(action string) func() (tea.Model, tea.Cmd) {
	m.setErrorStatusf("%s is not supported for remote sessions: %v", action, errRemoteActionUnsupported)

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) handleCreate() func() (tea.Model, tea.Cmd) {
	query := strings.TrimSpace(m.textInput.Value())
	if query == "" {
		return nil
	}

	cwd, err := m.deps.WorkingDirectory.GetWorkingDirectory()
	if err != nil {
		logTUIFailure(m.operationContext(), tuiOperationResolveWorkingDirectory, err)
		m.setErrorStatusf("getting current directory: %v", err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.createSession = query
	m.dialog = dialogInputCreateDir
	m.textInput.Blur()
	m.setCreateDirInputValue(session.DefaultCreateSessionDirectory(cwd, query, m.config.Settings.DefaultSessionPathSuggestion))
	m.refreshCreateDirSuggestions()

	return func() (tea.Model, tea.Cmd) {
		return m, m.createDirInput.Focus()
	}
}

func (m *Model) handleRename() func() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.filtered) {
		return nil
	}

	selected := m.filtered[m.cursor]
	if selected.IsSaved() {
		m.setErrorStatusf("Rename unavailable for saved sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginBridge {
		m.setErrorStatusf("Rename is not supported on origin sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginSSH {
		if m.deps.RemoteActions == nil {
			return m.rejectRemoteAction("Rename")
		}

		if !m.deps.RemoteActions.IsActionPermitted(m.config, selected, "rename") {
			m.setErrorStatusf("Remote rename is disabled by remote permissions")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}
	}

	if m.deps.Renamer == nil {
		logTUIFailure(m.operationContext(), tuiOperationRenameSession, errRenameUnavailable)
		m.setErrorStatusf("Rename failed: %v", errRenameUnavailable)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.renameTarget = selected
	m.dialog = dialogInputRename
	m.textInput.Blur()
	m.renameInput.SetValue("")

	return func() (tea.Model, tea.Cmd) { return m, m.renameInput.Focus() }
}

func (m *Model) handleKill() func() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.filtered) {
		return nil
	}

	selected := m.filtered[m.cursor]
	if selected.IsSaved() {
		m.setErrorStatusf("Kill unavailable for saved sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Attached {
		m.setErrorStatusf("Kill unavailable for attached sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginBridge {
		m.setErrorStatusf("Kill is not supported on origin sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginSSH {
		return m.prepareRemoteConfirmedAction(
			selected,
			"kill",
			"Kill",
			tuiOperationKillSession,
			m.config.Settings.ConfirmKill,
			dialogConfirmKill,
		)
	}

	if m.deps.Killer == nil {
		logTUIFailure(m.operationContext(), tuiOperationKillSession, errKillUnavailable)
		m.setErrorStatusf("Kill failed: %v", errKillUnavailable)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.killTarget = selected
	m.confirmCommand = nil
	m.confirmAction = func() error {
		if err := m.deps.Killer.KillSession(m.operationContext(), selected); err != nil {
			return oops.In("tui").With("operation", "kill_session").Wrap(err)
		}

		return nil
	}

	if !m.config.Settings.ConfirmKill {
		return m.runConfirmedAction("Kill failed", tuiOperationKillSession)
	}

	m.confirmYes = false
	m.dialog = dialogConfirmKill

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) handleDelete() func() (tea.Model, tea.Cmd) {
	if m.selectedRowIsAgent() {
		m.setErrorStatusf("Delete unavailable for agent rows")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if m.cursor >= len(m.filtered) {
		return nil
	}

	selected := m.filtered[m.cursor]
	if !selected.IsSaved() && selected.Attached {
		m.setErrorStatusf("Delete unavailable for attached sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginBridge {
		m.setErrorStatusf("Delete is not supported on origin sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginSSH {
		return m.prepareRemoteConfirmedAction(
			selected,
			"delete",
			"Delete",
			tuiOperationDeleteSession,
			m.config.Settings.ConfirmDelete,
			dialogConfirmDelete,
		)
	}

	if m.deps.Deleter == nil {
		logTUIFailure(m.operationContext(), tuiOperationDeleteSession, errDeleteUnavailable)
		m.setErrorStatusf("Delete failed: %v", errDeleteUnavailable)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.deleteTarget = selected
	m.configureLocalDeleteAction(selected)

	if !m.config.Settings.ConfirmDelete {
		return m.runConfirmedAction("Delete failed", tuiOperationDeleteSession)
	}

	m.confirmYes = false
	m.dialog = dialogConfirmDelete

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) prepareRemoteConfirmedAction(
	target session.Session,
	action string,
	actionLabel string,
	operation tuiLogOperation,
	confirm bool,
	dialog dialogState,
) func() (tea.Model, tea.Cmd) {
	if m.deps.RemoteActions == nil {
		return m.rejectRemoteAction(actionLabel)
	}

	if !m.deps.RemoteActions.IsActionPermitted(m.config, target, action) {
		m.setErrorStatusf("Remote %s is disabled by remote permissions", action)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.confirmAction = nil
	m.confirmCommand = m.runRemoteAction(operation, target, RemoteActionRequest{
		Action: action, NewName: "", Directory: "",
	})

	if !confirm {
		return m.runConfirmedAction(actionLabel+" failed", operation)
	}

	m.confirmYes = false
	m.dialog = dialog

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) configureLocalDeleteAction(target session.Session) {
	m.confirmCommand = nil
	m.confirmAction = func() error {
		result, err := m.deps.Deleter.DeleteSessionEverywhere(
			m.operationContext(),
			target,
			true,
		)
		if err != nil {
			return oops.In("tui").With("operation", "delete_session", "live_killed", result.LiveKilled, "saved_deleted", result.SavedDeleted).Wrap(err)
		}

		if !result.LiveKilled && !result.SavedDeleted {
			return oops.In("tui").Code("session_not_found").With("operation", "delete_session").Wrap(session.ErrSessionNotFound)
		}

		if err := m.updateState(m.operationContext(), func(current *session.State) {
			current.DeleteSessionIdentity(target.SemanticIdentity())
		}); err != nil {
			return oops.In("tui").With("operation", "save_state").Wrap(err)
		}

		m.moveCursorToPreviousRow()

		return nil
	}
}

func (m *Model) handleSnapshot() func() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.filtered) {
		return nil
	}

	selected := m.filtered[m.cursor]
	if selected.IsSaved() {
		m.setErrorStatusf("Snapshot unavailable for saved sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if selected.Origin == session.OriginSSH {
		if m.deps.RemoteActions == nil {
			return m.rejectRemoteAction("Snapshot")
		}

		if !m.deps.RemoteActions.IsActionPermitted(m.config, selected, "snapshot") {
			m.setErrorStatusf("Remote snapshot is disabled by remote permissions")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		return func() (tea.Model, tea.Cmd) {
			return m, m.runRemoteAction(tuiOperationSnapshotSession, selected, RemoteActionRequest{
				Action: "snapshot", NewName: "", Directory: "",
			})
		}
	}

	if selected.Origin != session.OriginLocal {
		return m.rejectRemoteAction("Snapshot")
	}

	if m.deps.Snapshots == nil {
		logTUIFailure(m.operationContext(), tuiOperationSnapshotSession, errSnapshotUnavailable)
		m.setErrorStatusf("Snapshot is not available: no snapshot service")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	target := selected
	ctx := m.operationContext()

	return func() (tea.Model, tea.Cmd) {
		return m, func() tea.Msg {
			if !m.lifecycle.BeginWorker() {
				return nil
			}
			defer m.lifecycle.EndWorker()

			if err := m.deps.Snapshots.SnapshotSession(ctx, target); err != nil {
				return sessionActionResultMsg{
					operation:      tuiOperationSnapshotSession,
					deferred:       false,
					attach:         false,
					err:            err,
					sessionName:    target.Name,
					target:         target,
					switchSession:  "",
					switchPane:     "",
					quit:           false,
					refresh:        false,
					statePersisted: false,
					agentView:      nil,
				}
			}

			return sessionActionResultMsg{
				operation:      tuiOperationSnapshotSession,
				deferred:       false,
				attach:         false,
				err:            nil,
				sessionName:    target.Name,
				target:         target,
				switchSession:  "",
				switchPane:     "",
				quit:           false,
				refresh:        true,
				statePersisted: false,
				agentView:      nil,
			}
		}
	}
}

func (m *Model) moveCursorToPreviousRow() {
	if m.cursor > 0 {
		m.cursor--
	}
}

func (m *Model) remoteStatusTickCommand() tea.Cmd {
	if !remoteSessionsEnabled(m.config, m.deps) {
		return nil
	}

	return tea.Tick(remoteStatusTickInterval, func(time.Time) tea.Msg { return remoteStatusTickMsg{now: time.Now()} })
}

func (m *Model) handleRemoteStatusTick(now time.Time) {
	if !remoteSessionsEnabled(m.config, m.deps) || len(m.remoteCatalog.Connections) == 0 {
		return
	}

	statuses := make(map[string]session.RemoteStatus, len(m.remoteCatalog.Connections))
	for _, connection := range m.remoteCatalog.Connections {
		statuses[connection.ID] = remoteConnectionStatus(connection, m.config.Remote.StaleAfter, now)
	}

	changed := false

	for index := range m.sessions {
		row := &m.sessions[index]
		if row.Origin != session.OriginSSH {
			continue
		}

		status, ok := statuses[row.RuntimeIdentity.ConnectionID]
		if !ok || row.RemoteStatus == status {
			continue
		}

		row.RemoteStatus = status
		changed = true
	}

	if !changed {
		return
	}

	selected := m.selectedSessionIdentity()
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}
