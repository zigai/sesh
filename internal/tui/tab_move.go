package tui

import (
	"fmt"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

// tabMoveState changes selection eligibility, not the display mode or visible agent rows.
// The source subject is captured before the cursor is repurposed for destination selection.
type tabMoveState struct {
	subject        action.Subject
	candidates     []session.AgentSession
	label          string
	selection      sessionIdentity
	query          string
	queryPosition  int
	listTop        int
	viewportPolicy listViewportPolicy
	focusedSession string
	pending        bool
	destination    *session.Session
}

func (m *Model) tabMoveSubject() (action.Subject, bool) {
	subject, ok := m.actionSubject(action.SessionMoveWindow.Describe())
	if !ok || subject.At.Kind != action.LocationLocal || len(subject.Sessions) != 1 ||
		subject.Pane == "" || subject.Sessions[0].ID == "" || subject.Sessions[0].Saved() ||
		inboxSyntheticSession(subject.Sessions[0]) {
		return subject, false
	}

	return subject, m.actionAvailable(action.SessionMoveWindow.Describe(), subject) == nil
}

func (m *Model) beginTabMove(subject action.Subject) func() (tea.Model, tea.Cmd) {
	return func() (tea.Model, tea.Cmd) {
		selected := m.selectedSessionIdentity()
		label := subject.Pane

		if row := m.selectedListRow(); row.isAgent() && row.sessionIndex < len(m.filtered) {
			agent := m.filtered[row.sessionIndex].Agents[row.agentIndex]
			if agent.Task != "" {
				label = agent.Task
			}
		}

		m.tabMove = &tabMoveState{
			subject:        subject,
			candidates:     m.tabMoveCandidates(subject),
			label:          label,
			selection:      selected,
			query:          m.textInput.Value(),
			queryPosition:  m.textInput.Position(),
			listTop:        m.listTop,
			viewportPolicy: m.listViewportPolicy,
			focusedSession: m.focusedSession,
			pending:        false,
			destination:    nil,
		}
		m.closeMenu()
		m.clearSessionFocus()
		m.sessionPrefixPending = false
		m.footerHoverDesc = ""
		m.clearStatus()

		m.refreshDisplaySessions()
		m.filterSessions()

		m.normalizeTabMoveCursor()

		return m, nil
	}
}

func inboxSyntheticSession(row session.Session) bool {
	return strings.HasPrefix(row.ID, inboxGroupIDPrefix)
}

// Inbox groups are not tmux sessions. Keep those groups intact and add actual session
// destinations below them, without duplicating their agent rows.
func (m *Model) appendTabMoveSessions() {
	for _, row := range m.sessions {
		if row.Saved() || inboxSyntheticSession(row) {
			continue
		}

		row.Agents = nil
		m.displaySessions = append(m.displaySessions, row)
	}
}

func (m *Model) tabMoveDestination(row popupListRow) bool {
	if m.tabMove == nil || row.isAgent() || row.isSeparator() || row.sessionIndex < 0 ||
		row.sessionIndex >= len(m.filtered) {
		return false
	}

	target := m.filtered[row.sessionIndex]
	source := m.tabMove.subject.Sessions[0]

	return !target.Saved() && target.ID != "" && !inboxSyntheticSession(target) &&
		action.LocationOf(target).Kind == action.LocationLocal && target.ID != source.ID &&
		sameServerSocket(target.SemanticIdentity().Socket, source.SemanticIdentity().Socket)
}

func (m *Model) normalizeTabMoveCursor() {
	m.agentCursor = parentAgentIndex
	if m.tabMoveDestination(m.selectedListRow()) {
		return
	}

	m.moveSelectionByStep(1)
}

func (m *Model) restoreTabMoveDestination(selected sessionIdentity) {
	for index, target := range m.filtered {
		row := popupListRow{sessionIndex: index, agentIndex: parentAgentIndex}
		if target.ID == selected.id && m.tabMoveDestination(row) {
			m.cursor = index
			break
		}
	}

	m.normalizeTabMoveCursor()
}

func (m *Model) handleTabMoveKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.tabMove.pending {
		return m, nil
	}

	pressed := keyPressFromMsg(msg)

	keys := m.actionKeys()
	switch {
	case pressed == "esc", pressed == "ctrl+c":
		return m.cancelTabMove()
	case bubbleskey.Matches(pressed, keys.enter):
		return m.confirmTabMove()
	case bubbleskey.Matches(pressed, keys.groupUp):
		m.moveSelectionByStep(-1)
	case bubbleskey.Matches(pressed, keys.groupDown):
		m.moveSelectionByStep(1)
	case m.handleJumpNavigation(pressed, keys), m.handlePageNavigation(pressed, keys),
		m.handleStepNavigation(pressed, keys):
	default:
		// No normal row actions, mode switching, session shortcuts or menus while
		// choosing a destination. Unbound typing still filters the current view.
		return m.handleSearchInput(msg)()
	}

	return m, nil
}

func (m *Model) cancelTabMove() (tea.Model, tea.Cmd) {
	move := m.tabMove
	m.tabMove = nil
	m.textInput.SetValue(move.query)
	m.textInput.SetCursor(move.queryPosition)
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(move.selection)
	m.listTop = move.listTop
	m.listViewportPolicy = move.viewportPolicy
	m.focusedSession = move.focusedSession
	m.footerHoverDesc = ""
	m.clearStatus()

	return m, nil
}

func (m *Model) confirmTabMove() (tea.Model, tea.Cmd) {
	if m.tabMove.pending {
		return m, nil
	}

	if !m.tabMoveDestination(m.selectedListRow()) {
		m.setErrorStatusf("Select a live destination session")
		return m, nil
	}

	target := m.filtered[m.cursor]
	m.tabMove.destination = &target

	model, cmd := m.runAction(action.SessionMoveWindow, m.tabMove.subject,
		action.Values{"destination": {target.ID}})()
	if cmd != nil {
		m.tabMove.pending = true
		m.tabMove.destination = &target
	}

	return model, cmd
}

func (m *Model) tabMoveInstruction() string {
	if m.tabMove.pending {
		return fmt.Sprintf("Moving %q…", m.tabMove.label)
	}

	if len(m.popupSelectableRows()) == 0 {
		return "No destination sessions match — edit search or esc cancel"
	}

	return fmt.Sprintf("Move %q from %s — select destination session",
		m.tabMove.label, m.tabMove.subject.Sessions[0].Name)
}

func (m *Model) finishTabMove(msg actionResultMsg) tea.Cmd {
	move := m.tabMove
	if move == nil {
		return nil
	}

	moved, _ := msg.output.(action.MovedWindow)
	m.recordAgentRelocations(moved, move.candidates)

	if moved.Moved && m.state != nil {
		m.state.MoveAgentViews(moved.Source.Session(action.Local()).SemanticIdentity(),
			moved.Destination.Session(action.Local()).SemanticIdentity(), moved.PaneIDs)
	}

	if msg.err != nil && !moved.Moved && action.CodeOf(msg.err) != action.CodeUncertain {
		move.pending = false

		m.setErrorStatusf("Move failed: %v", msg.err)

		return m.refreshSessions()
	}

	// Discard polls and catalog loads sampled before this mutation completed.
	m.agentSummaryObservation++
	m.tabMoveObservation++

	// A completed move with a history error, or an uncertain mutation, must not
	// leave Enter armed to submit the same operation again.
	m.cancelTabMove()
	m.clearSessionFocus()

	if msg.err == nil || moved.Moved {
		selected := move.selection
		selected.runtimeKey = move.destination.RuntimeKey()
		selected.id = move.destination.ID
		selected.name = move.destination.Name
		selected.kind = move.destination.Kind
		m.movedAgentSelection = &selected
	}

	if msg.err != nil {
		m.setErrorStatusf("Move incomplete: %v", msg.err)
	} else {
		m.statusMessage = fmt.Sprintf("Moved %s to %s", move.label, move.destination.Name)
		m.statusLevel = statusNone
	}

	return m.refreshSessions()
}

func (m *Model) restoreMovedAgentSelection() {
	if m.movedAgentSelection == nil {
		return
	}

	selected := *m.movedAgentSelection

	row, found := m.findMovedAgentRow(selected)
	if !found && m.textInput.Value() != "" && m.movedAgentInCatalog(selected) {
		// A query matching the old session name must not hide the moved agent.
		m.textInput.SetValue("")
		m.refreshDisplaySessions()
		m.filterSessions()
		row, found = m.findMovedAgentRow(selected)
	}

	if found {
		m.selectListRow(row)
		m.movedAgentSelection = nil
	}
}

func (m *Model) findMovedAgentRow(selected sessionIdentity) (popupListRow, bool) {
	for sessionIndex, group := range m.filtered {
		for agentIndex, agent := range group.Agents {
			row := popupListRow{sessionIndex: sessionIndex, agentIndex: agentIndex}

			owner := group
			if item, ok := m.inboxItemAt(row); ok {
				owner = item.owner
			}

			if selected.matchesSession(owner) && movedAgentMatches(agent, selected) {
				return row, true
			}
		}
	}

	return emptyPopupListRow(), false
}

func (m *Model) movedAgentInCatalog(selected sessionIdentity) bool {
	for _, owner := range m.sessions {
		if selected.matchesSession(owner) &&
			slices.ContainsFunc(owner.Agents, func(agent session.AgentSession) bool {
				return movedAgentMatches(agent, selected)
			}) {
			return true
		}
	}

	return false
}

func movedAgentMatches(agent session.AgentSession, selected sessionIdentity) bool {
	return agent.Host == selected.host && agent.PaneID == selected.paneID &&
		(selected.agentID == "" || agent.RegistryID == selected.agentID)
}

func (m *Model) tabMoveFooterParts(width int) []footerPart {
	parts := []footerPart{
		newFooterPart(m.actionKeys().enter.Help().Key, "move"),
		newFooterPart("esc", "cancel"),
	}
	if m.tabMove.pending {
		return []footerPart{newFooterPart("", "Moving tab…")}
	}

	if width >= compactFooterMaxWidth {
		parts = append([]footerPart{newFooterPart("↑/↓", "sessions")}, parts...)
	}

	remaining := width - footerPartsWidth(parts) - footerPartSpacing - 1
	if remaining > 0 {
		instruction := SanitizeTerminalText(m.tabMoveInstruction())
		if remaining < minSearchWidth {
			instruction = "pick session"
			if len(m.popupSelectableRows()) == 0 {
				instruction = "no matches"
			}
		}

		parts = append(parts, newFooterPart("", truncateStyled(instruction, remaining)))
	}

	return parts
}

func (m *Model) blocksTabMoveClick(mouse tea.Mouse) bool {
	return m.tabMove != nil && (m.tabMove.pending || mouse.Button != tea.MouseLeft)
}
