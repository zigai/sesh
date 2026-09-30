package tui

import tea "charm.land/bubbletea/v2"

func (m *Model) handleSearchKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	previous := m.textInput.Value()

	var tiCmd tea.Cmd

	m.textInput, tiCmd = m.textInput.Update(msg)
	if m.textInput.Value() != previous {
		m.listViewportPolicy = listViewportFollowSelection
		m.filterSessions()
		m.cursor = 0
		m.agentCursor = parentAgentIndex
		m.clearStatus()
	}

	return func() (tea.Model, tea.Cmd) { return m, tiCmd }
}
