package tui

const (
	parentAgentIndex    = -1
	separatorAgentIndex = -2
)

const (
	listViewportFollowSelection listViewportPolicy = iota
	listViewportPreserveTop
)

type listViewportPolicy uint8

type popupListRow struct {
	sessionIndex int
	agentIndex   int
}

func (row popupListRow) isAgent() bool {
	return row.agentIndex >= 0
}

func (row popupListRow) isSeparator() bool {
	return row.agentIndex == separatorAgentIndex
}

func (row popupListRow) equal(other popupListRow) bool {
	return row.sessionIndex == other.sessionIndex && row.agentIndex == other.agentIndex
}

func (m *Model) selectedListRow() popupListRow {
	row := popupListRow{sessionIndex: m.cursor, agentIndex: parentAgentIndex}
	if m.displayMode != displayModeAgents || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return row
	}

	agents := m.filtered[m.cursor].Agents
	if len(agents) == 0 {
		return row
	}

	if m.agentCursor == parentAgentIndex {
		return row
	}

	row.agentIndex = m.agentCursor
	if row.agentIndex < 0 || row.agentIndex >= len(agents) {
		row.agentIndex = 0
	}

	return row
}

func (m *Model) selectListRow(row popupListRow) {
	if row.isSeparator() {
		return
	}

	m.cursor = row.sessionIndex
	m.listViewportPolicy = listViewportFollowSelection
	m.initialAgentSelectionPending = false

	m.agentCursor = parentAgentIndex
	if row.isAgent() {
		m.agentCursor = row.agentIndex
	}
}

func (m *Model) selectedRowIsAgent() bool {
	return m.selectedListRow().isAgent()
}

func selectedVisualIndex(rows []popupListRow, selected popupListRow) int {
	for index, row := range rows {
		if row.equal(selected) {
			return index
		}
	}

	return -1
}

func (m *Model) popupListRows() []popupListRow {
	m.listRows = m.listRows[:0]

	if m.displayMode != displayModeAgents {
		for sessionIndex := range m.filtered {
			m.listRows = append(m.listRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   parentAgentIndex,
			})
		}

		return m.listRows
	}

	hasAgentSessions := false
	separatorInserted := false

	for sessionIndex, row := range m.filtered {
		if len(row.Agents) > 0 {
			hasAgentSessions = true

			m.listRows = append(m.listRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   parentAgentIndex,
			})

			for agentIndex := range row.Agents {
				m.listRows = append(m.listRows, popupListRow{
					sessionIndex: sessionIndex,
					agentIndex:   agentIndex,
				})
			}
		} else {
			if hasAgentSessions && !separatorInserted {
				m.listRows = append(m.listRows, popupListRow{
					sessionIndex: sessionIndex,
					agentIndex:   separatorAgentIndex,
				})
				separatorInserted = true
			}

			m.listRows = append(m.listRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   parentAgentIndex,
			})
		}
	}

	return m.listRows
}

func (m *Model) popupSelectableRows() []popupListRow {
	m.selectableRows = m.selectableRows[:0]

	for sessionIndex, row := range m.filtered {
		if m.displayMode != displayModeAgents {
			m.selectableRows = append(m.selectableRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   parentAgentIndex,
			})

			continue
		}

		if len(row.Agents) == 0 {
			m.selectableRows = append(m.selectableRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   parentAgentIndex,
			})

			continue
		}

		for agentIndex := range row.Agents {
			m.selectableRows = append(m.selectableRows, popupListRow{
				sessionIndex: sessionIndex,
				agentIndex:   agentIndex,
			})
		}
	}

	return m.selectableRows
}

func (m *Model) rowSelectable(row popupListRow) bool {
	if row.isSeparator() {
		return false
	}

	if row.sessionIndex >= 0 && row.sessionIndex < len(m.filtered) {
		if !row.isAgent() {
			return true
		}

		return row.agentIndex < len(m.filtered[row.sessionIndex].Agents)
	}

	return false
}

func (m *Model) selectedSelectableIndex(rows []popupListRow) int {
	return selectedVisualIndex(rows, m.selectedListRow())
}

func (m *Model) visibleListRows(height int) []popupListRow {
	rows := m.popupListRows()
	if len(rows) == 0 || height <= 0 {
		return nil
	}

	start := m.visibleListStart(rows, height)
	m.listTop = start
	end := min(start+height, len(rows))

	return rows[start:end]
}

func (m *Model) visibleListStart(rows []popupListRow, height int) int {
	if len(rows) == 0 || height <= 0 {
		return 0
	}

	maxTop := max(len(rows)-height, 0)

	start := min(max(m.listTop, 0), maxTop)
	if m.listViewportPolicy == listViewportPreserveTop {
		start = alignListStartToSelection(rows, start, height, m.selectedListRow())
		return min(max(start, 0), maxTop)
	}

	groupStart, groupEnd := selectedListRange(rows, m.selectedListRow())
	if groupStart >= 0 {
		start = alignListStartToGroup(start, height, groupStart, groupEnd)
	}

	start = alignListStartToSelection(rows, start, height, m.selectedListRow())

	start = alignListStartToParent(rows, start, m.cursor, maxTop)

	return min(max(start, 0), maxTop)
}

func alignListStartToSelection(rows []popupListRow, start int, height int, selected popupListRow) int {
	selectedIndex := selectedVisualIndex(rows, selected)
	if selectedIndex < 0 {
		return start
	}

	switch {
	case selectedIndex < start:
		return selectedIndex
	case selectedIndex >= start+height:
		return selectedIndex - height + 1
	default:
		return start
	}
}

func selectedListRange(rows []popupListRow, selected popupListRow) (int, int) {
	selectedIndex := selectedVisualIndex(rows, selected)
	if selectedIndex < 0 {
		return -1, -1
	}

	if selected.isAgent() {
		if selected.agentIndex == 0 && selectedIndex > 0 {
			parent := rows[selectedIndex-1]
			if !parent.isAgent() && parent.sessionIndex == selected.sessionIndex {
				return selectedIndex - 1, selectedIndex + 1
			}
		}

		return selectedIndex, selectedIndex + 1
	}

	return selectedGroupRange(rows, selected.sessionIndex)
}

func alignListStartToParent(rows []popupListRow, start int, selectedSession int, maxTop int) int {
	if start < 0 || start >= len(rows) || !rows[start].isAgent() || rows[start].sessionIndex == selectedSession {
		return start
	}

	for index := start + 1; index < len(rows) && index <= maxTop; index++ {
		if !rows[index].isAgent() {
			return index
		}
	}

	return start
}

func alignListStartToGroup(start int, height int, groupStart int, groupEnd int) int {
	switch {
	case groupFullyVisible(start, height, groupStart, groupEnd):
		return start
	case groupEnd-groupStart <= height:
		if groupStart < start {
			return groupStart
		}

		return groupEnd - height
	case rangesOverlap(start, start+height, groupStart, groupEnd):
		return start
	case groupStart < start:
		return groupStart
	default:
		return groupStart
	}
}

func groupFullyVisible(start int, height int, groupStart int, groupEnd int) bool {
	return groupStart >= start && groupEnd <= start+height
}

func rangesOverlap(leftStart int, leftEnd int, rightStart int, rightEnd int) bool {
	return rightEnd > leftStart && rightStart < leftEnd
}

func selectedGroupRange(rows []popupListRow, sessionIndex int) (int, int) {
	start := -1

	for index, row := range rows {
		if row.sessionIndex == sessionIndex {
			if start < 0 {
				start = index
			}

			continue
		}

		if start >= 0 {
			return start, index
		}
	}

	if start < 0 {
		return -1, -1
	}

	return start, len(rows)
}
