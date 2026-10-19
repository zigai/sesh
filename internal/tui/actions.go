package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

var (
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
		if outcomeLogged(msg.err) {
			return
		}

		logTUIFailure(m.operationContext(), msg.operation, msg.err)

		return
	}

	if msg.deferred {
		switch msg.operation {
		case tuiOperationEnterLiveSession, tuiOperationActivateAgentPane:
			return
		case tuiOperationLoadCatalog,
			tuiOperationLoadRemoteCatalog,
			tuiOperationLoadOriginSessions,
			tuiOperationLoadAgentSummaries,
			tuiOperationRestoreSavedSession,
			tuiOperationCreateSession,
			tuiOperationRenameSession,
			tuiOperationKillSession,
			tuiOperationDeleteSession,
			tuiOperationResolveWorkingDirectory,
			tuiOperationSaveState,
			tuiOperationSnapshotSession,
			tuiOperationAgentGitScan,
			tuiOperationRunAction,
			tuiOperationCustomFieldRun,
			tuiOperationSaveInbox,
			tuiOperationOpen,
			tuiOperationLoadEvidence:
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
		m.setErrorStatusf("%v", msg.err)

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

	if m.displayMode == displayModeInbox {
		return m.handleInboxEnter(selected)
	}

	if selectedSession.Origin == session.OriginSSH {
		return m.handleRemoteEnter(selectedSession, selected)
	}

	if selected.isAgent() && selectedSession.Origin != session.OriginBridge {
		agent := selectedSession.Agents[selected.agentIndex]

		return func() (tea.Model, tea.Cmd) {
			return m, m.runAgentSessionAction(selectedSession, agent)
		}
	}

	return m.openSession(selectedSession)
}

func (m *Model) openSession(target session.Session) func() (tea.Model, tea.Cmd) {
	switch target.Origin {
	case session.OriginSSH:
		return m.handleRemoteEnter(target, popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex})
	case session.OriginBridge:
		return m.runAction(action.SessionSwitch, action.SubjectOf(target), action.Values{})
	case session.OriginLocal:
	}

	return func() (tea.Model, tea.Cmd) {
		if target.Saved() {
			return m.runAction(action.SnapshotRestore, action.SubjectOf(target), action.Values{})()
		}

		return m, m.runLiveSessionAction(target)
	}
}

func (m *Model) handleRemoteEnter(
	target session.Session,
	selectedRow popupListRow,
) func() (tea.Model, tea.Cmd) {
	if target.Saved() {
		return m.runAction(action.SnapshotRestore, action.SubjectOf(target), action.Values{})
	}

	paneID := ""
	if selectedRow.isAgent() && selectedRow.agentIndex < len(target.Agents) {
		paneID = target.Agents[selectedRow.agentIndex].PaneID
	}

	return m.attachRemote(target, paneID)
}

func (m *Model) attachRemote(target session.Session, paneID string) func() (tea.Model, tea.Cmd) {
	if m.deps.RemoteAttach == nil {
		return m.rejectRemoteAction("Enter")
	}

	return func() (tea.Model, tea.Cmd) {
		m.deferredRemoteAttach = target
		m.remoteAttachPane = paneID
		m.hasDeferredRemoteAttach = true

		return m, tea.Quit
	}
}

func (m *Model) rejectRemoteAction(operation string) func() (tea.Model, tea.Cmd) {
	m.setErrorStatusf("%s is not supported for remote sessions: %v", operation, errRemoteActionUnsupported)

	return func() (tea.Model, tea.Cmd) { return m, nil }
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
