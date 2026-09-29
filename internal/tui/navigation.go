package tui

import (
	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleNavigationKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	pressed := keyPressFromMsg(msg)
	keys := newActionKeyMap(m.config.Keys)

	switch {
	case bubbleskey.Matches(pressed, keys.close):
		return func() (tea.Model, tea.Cmd) { return m, tea.Quit }
	case bubbleskey.Matches(pressed, keys.handoff):
		m.replayKey = ReplayKeyFloatingTerminal

		return func() (tea.Model, tea.Cmd) { return m, tea.Quit }
	case m.handleAgentGroupNavigation(pressed, keys),
		m.handleJumpNavigation(pressed, keys),
		m.handlePageNavigation(pressed, keys),
		m.handleStepNavigation(pressed, keys):
		m.clearStatus()
		return func() (tea.Model, tea.Cmd) { return m, nil }
	default:
		return nil
	}
}

func (m *Model) handleAgentGroupNavigation(pressed keyPressSpec, keys actionKeyMap) bool {
	if m.displayMode != displayModeAgents {
		return false
	}

	switch {
	case bubbleskey.Matches(pressed, keys.groupUp):
		m.moveAgentGroup(-1)
		return true
	case bubbleskey.Matches(pressed, keys.groupDown):
		m.moveAgentGroup(1)
		return true
	default:
		return false
	}
}

func (m *Model) handleJumpNavigation(pressed keyPressSpec, keys actionKeyMap) bool {
	if bubbleskey.Matches(pressed, keys.top) {
		m.selectListBoundary(false)
		return true
	}

	if bubbleskey.Matches(pressed, keys.bottom) {
		m.selectListBoundary(true)
		return true
	}

	return false
}

func (m *Model) handlePageNavigation(pressed keyPressSpec, keys actionKeyMap) bool {
	if bubbleskey.Matches(pressed, keys.pageUp) {
		m.moveSelectionByPage(-1)
		return true
	}

	if bubbleskey.Matches(pressed, keys.pageDown) {
		m.moveSelectionByPage(1)
		return true
	}

	return false
}

func (m *Model) handleStepNavigation(pressed keyPressSpec, keys actionKeyMap) bool {
	switch {
	case bubbleskey.Matches(pressed, keys.stepUp):
		m.moveSelectionByStep(-1)
		return true
	case bubbleskey.Matches(pressed, keys.stepDown):
		m.moveSelectionByStep(1)
		return true
	default:
		return false
	}
}

func (m *Model) selectListBoundary(last bool) {
	rows := m.popupSelectableRows()
	if len(rows) == 0 {
		return
	}

	index := 0
	if last {
		index = len(rows) - 1
	}

	m.selectListRow(rows[index])
}

func (m *Model) moveSelectionByStep(direction int) {
	rows := m.popupSelectableRows()
	if len(rows) == 0 || direction == 0 {
		return
	}

	if index := m.selectedSelectableIndex(rows); index >= 0 {
		index = (index + direction + len(rows)) % len(rows)
		m.selectListRow(rows[index])

		return
	}

	visualRows := m.popupListRows()

	selectedIndex := selectedVisualIndex(visualRows, m.selectedListRow())
	if selectedIndex < 0 {
		if direction > 0 {
			m.selectListRow(rows[0])
		} else {
			m.selectListRow(rows[len(rows)-1])
		}

		return
	}

	for offset := 1; offset <= len(visualRows); offset++ {
		index := (selectedIndex + direction*offset + len(visualRows)) % len(visualRows)

		candidate := visualRows[index]
		if selectedVisualIndex(rows, candidate) >= 0 {
			m.selectListRow(candidate)
			return
		}
	}
}

func (m *Model) moveSelectionByPage(direction int) {
	rows := m.popupSelectableRows()
	if len(rows) == 0 || direction == 0 {
		return
	}

	layout := m.calcPopupLayout(m.hasRenderedStatus())
	pageSize := 0

	for _, row := range m.visibleListRows(layout.listHeight) {
		if selectedVisualIndex(rows, row) >= 0 {
			pageSize++
		}
	}

	index := max(m.selectedSelectableIndex(rows), 0)

	index = min(max(index+direction*max(pageSize, 1), 0), len(rows)-1)
	m.selectListRow(rows[index])
}

func (m *Model) moveAgentGroup(direction int) {
	if m.displayMode != displayModeAgents || len(m.filtered) == 0 || direction == 0 {
		return
	}

	m.cursor = (m.cursor + direction + len(m.filtered)) % len(m.filtered)
	m.agentCursor = parentAgentIndex
}
