package tui

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	displayModeSesh        displayMode = "sesh"
	displayModeAgents      displayMode = "agents"
	displayModeInbox       displayMode = "inbox"
	minDisplayModesToCycle             = 2
)

type displayMode string

type sessionIdentity struct {
	runtimeKey string
	id         string
	name       string
	kind       session.Kind
	agentID    string
	paneID     string
	host       string
}

func emptySessionIdentity() sessionIdentity {
	var identity sessionIdentity

	return identity
}

func parseDisplayMode(value string) displayMode {
	value = strings.TrimSpace(value)

	switch {
	case strings.EqualFold(value, string(displayModeAgents)):
		return displayModeAgents
	case strings.EqualFold(value, string(displayModeInbox)):
		return displayModeInbox
	default:
		return displayModeSesh
	}
}

func (mode displayMode) tree() bool {
	return mode == displayModeAgents || mode == displayModeInbox
}

func (mode displayMode) label() string {
	return strings.ToUpper(string(mode))
}

func configuredDisplayModes(cfg *config.Config) []displayMode {
	if cfg == nil {
		return displayModesFrom(nil)
	}

	return displayModesFrom(cfg.Settings.DisplayModes)
}

func displayModesFrom(values []string) []displayMode {
	modes := make([]displayMode, 0, len(values))
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case string(displayModeSesh):
			modes = append(modes, displayModeSesh)
		case string(displayModeAgents):
			modes = append(modes, displayModeAgents)
		case string(displayModeInbox):
			modes = append(modes, displayModeInbox)
		}
	}

	if len(modes) == 0 {
		return []displayMode{displayModeSesh, displayModeAgents, displayModeInbox}
	}

	return modes
}

func initialDisplayMode(persisted string, modes []displayMode) displayMode {
	selected := parseDisplayMode(persisted)
	if slices.Contains(modes, selected) {
		return selected
	}

	return modes[0]
}

func (m *Model) cycleDisplayMode() bool {
	if len(m.displayModes) < minDisplayModesToCycle {
		return false
	}

	m.initialAgentSelectionPending = false
	m.listViewportPolicy = listViewportFollowSelection
	m.clearSessionFocus()

	previousMode := m.displayMode
	selected := m.selectedSessionIdentity()
	current := 0

	for index, mode := range m.displayModes {
		if mode == m.displayMode {
			current = index
			break
		}
	}

	if selected.empty() {
		selected = m.carriedSelection
	}

	m.carriedSelection = selected
	m.displayMode = m.displayModes[(current+1)%len(m.displayModes)]

	if m.state != nil {
		m.state.PopupDisplayMode = string(m.displayMode)
	}

	m.refreshDisplaySessions()
	m.filterSessions()

	if previousMode != displayModeAgents &&
		m.displayMode == displayModeAgents &&
		m.restoreCurrentSession() {
		return true
	}

	m.restoreSelectedSession(selected)

	return true
}

func (m *Model) showsDisplayModeIndicator() bool {
	return len(m.displayModes) > 1
}

func (m *Model) refreshDisplaySessions() {
	if m.displayMode == displayModeInbox {
		m.displaySessions = m.inboxSessions()
		m.inboxGroupKeys = inboxGroupKeys(m.displaySessions, m.sessionKeys.blocked)
		m.markCustomFieldsDirty()

		return
	}

	displayed := make([]session.Session, 0, len(m.sessions))
	for _, row := range m.sessions {
		row.Agents = slices.Clone(row.Agents)
		m.applyCachedAgentGit(&row)
		sortAgentSessions(row.Agents, m.agentSort)

		switch m.displayMode {
		case displayModeSesh:
			if !row.Current {
				displayed = append(displayed, row)
			}
		case displayModeAgents:
			if !row.Saved() {
				displayed = append(displayed, row)
			}
		case displayModeInbox:
			displayed = append(displayed, row)
		}
	}

	if m.displayMode == displayModeAgents {
		slices.SortStableFunc(displayed, compareAgentsModeSessions)
	}

	m.displaySessions = displayed
	m.markCustomFieldsDirty()
}

func compareAgentSessionRows(left session.Session, right session.Session) int {
	leftCreated := left.RuntimeIdentity.TmuxSessionCreatedAt
	rightCreated := right.RuntimeIdentity.TmuxSessionCreatedAt

	if !leftCreated.IsZero() && !rightCreated.IsZero() {
		if diff := leftCreated.Compare(rightCreated); diff != 0 {
			return diff
		}
	}

	leftNum, leftOK := parseTmuxSessionID(left.ID)
	rightNum, rightOK := parseTmuxSessionID(right.ID)

	if leftOK && rightOK {
		if diff := cmp.Compare(leftNum, rightNum); diff != 0 {
			return diff
		}
	}

	if !left.Activity.IsZero() && !right.Activity.IsZero() {
		if diff := left.Activity.Compare(right.Activity); diff != 0 {
			return diff
		}
	}

	for _, labels := range [][2]string{
		{left.Name, right.Name},
		{left.TmuxServerSocket, right.TmuxServerSocket},
		{left.ID, right.ID},
	} {
		if byLabel := compareFoldedLabels(labels[0], labels[1]); byLabel != 0 {
			return byLabel
		}
	}

	return 0
}

func parseTmuxSessionID(id string) (int, bool) {
	trimmed := strings.TrimPrefix(id, "$")

	num, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}

	return num, true
}

func compareAgentsModeSessions(left session.Session, right session.Session) int {
	leftHasAgents := len(left.Agents) > 0
	rightHasAgents := len(right.Agents) > 0

	if leftHasAgents && !rightHasAgents {
		return -1
	}

	if !leftHasAgents && rightHasAgents {
		return 1
	}

	return compareAgentSessionRows(left, right)
}

func compareFoldedLabels(left string, right string) int {
	if byFoldedLabel := strings.Compare(strings.ToLower(left), strings.ToLower(right)); byFoldedLabel != 0 {
		return byFoldedLabel
	}

	return strings.Compare(left, right)
}

func (m *Model) selectedSessionIdentity() sessionIdentity {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return emptySessionIdentity()
	}

	selected := m.selectedListRow()
	if found, ok := m.inboxItemAt(selected); ok {
		return sessionIdentity{
			runtimeKey: found.owner.RuntimeKey(),
			id:         found.owner.ID,
			name:       found.owner.Name,
			kind:       found.owner.Kind,
			agentID:    found.agent.RegistryID,
			paneID:     found.agent.PaneID,
			host:       found.agent.Host,
		}
	}

	row := m.filtered[m.cursor]
	agentID := ""
	paneID := ""

	host := ""

	if selected.isAgent() && selected.agentIndex < len(row.Agents) {
		agentID = row.Agents[selected.agentIndex].RegistryID
		paneID = row.Agents[selected.agentIndex].PaneID
		host = row.Agents[selected.agentIndex].Host
	}

	return sessionIdentity{
		runtimeKey: row.RuntimeKey(),
		id:         row.ID,
		name:       row.Name,
		kind:       row.Kind,
		agentID:    agentID,
		paneID:     paneID,
		host:       host,
	}
}

func (m *Model) restoreSelectedSession(selected sessionIdentity) {
	if selected.empty() {
		m.restoreCurrentSession()
		return
	}

	if m.displayMode == displayModeInbox {
		m.restoreSelectedInboxItem(selected)
		return
	}

	index := slices.IndexFunc(m.filtered, selected.matchesSession)
	if index < 0 {
		m.normalizeFilteredCursor()
		return
	}

	m.cursor = index

	m.agentCursor = parentAgentIndex
	if m.displayMode.tree() {
		m.agentCursor = selectedAgentIndex(m.filtered[index].Agents, selected)
	}
}

func (m *Model) restoreSelectedInboxItem(selected sessionIdentity) {
	for index, group := range m.filtered {
		agentIndex := slices.IndexFunc(group.Agents, func(agent session.AgentSession) bool {
			return agent.Host == selected.host && ((selected.paneID != "" && agent.PaneID == selected.paneID) ||
				(selected.agentID != "" && agent.RegistryID == selected.agentID))
		})
		if agentIndex >= 0 {
			m.cursor = index
			m.agentCursor = agentIndex

			return
		}
	}

	m.normalizeFilteredCursor()
}

func (selected sessionIdentity) empty() bool {
	return selected.runtimeKey == "" && selected.id == "" && selected.name == ""
}

func (m *Model) restoreCurrentSession() bool {
	if m.displayMode != displayModeAgents {
		return false
	}

	currentIndex := slices.IndexFunc(m.filtered, func(s session.Session) bool { return s.Current })
	if currentIndex < 0 {
		return false
	}

	m.cursor = currentIndex
	m.restoreCurrentAgent(m.filtered[currentIndex])

	return true
}

func (m *Model) restoreCurrentAgent(current session.Session) {
	m.agentCursor = parentAgentIndex

	if len(current.Agents) == 0 {
		return
	}

	m.agentCursor = 0

	if current.ActivePaneID == "" {
		return
	}

	if activeIndex := slices.IndexFunc(current.Agents, func(agent session.AgentSession) bool {
		return agent.PaneID == current.ActivePaneID
	}); activeIndex >= 0 {
		m.agentCursor = activeIndex
	}
}

func (m *Model) settleInitialAgentSelection() {
	if !m.initialAgentSelectionPending || m.displayMode != displayModeAgents || m.textInput.Value() != "" {
		return
	}

	currentIndex := slices.IndexFunc(m.filtered, func(row session.Session) bool { return row.Current })
	if currentIndex < 0 || len(m.filtered[currentIndex].Agents) == 0 || m.cursor != currentIndex {
		return
	}

	if m.agentCursor == parentAgentIndex {
		m.restoreCurrentAgent(m.filtered[currentIndex])
	}

	m.initialAgentSelectionPending = false
}

func (selected sessionIdentity) matchesSession(row session.Session) bool {
	if selected.runtimeKey != "" {
		return row.RuntimeKey() == selected.runtimeKey
	}

	if selected.id != "" {
		return row.ID == selected.id
	}

	return row.Name == selected.name && row.Kind == selected.kind
}

func selectedAgentIndex(agents []session.AgentSession, selected sessionIdentity) int {
	if selected.agentID != "" {
		if idx := slices.IndexFunc(agents, func(agent session.AgentSession) bool {
			return agent.RegistryID == selected.agentID
		}); idx >= 0 {
			return idx
		}
	}

	if selected.paneID != "" {
		if idx := slices.IndexFunc(agents, func(agent session.AgentSession) bool {
			return agent.PaneID == selected.paneID
		}); idx >= 0 {
			return idx
		}
	}

	if selected.agentID != "" || selected.paneID != "" {
		if len(agents) > 0 {
			return 0
		}
	}

	return parentAgentIndex
}
