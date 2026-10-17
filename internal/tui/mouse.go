package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	mouseWheelRows    = 3
	mouseBorderMargin = 2
	footerPartSpacing = 2
)

type popupMouseLayout struct {
	layout     popupLayout
	bodyX      int
	inputX     int
	inputY     int
	inputWidth int
	listY      int
	footerY    int
	topY       int
	bottomY    int
}

func (m *Model) handleMouseClick(mouse tea.Mouse) func() (tea.Model, tea.Cmd) {
	if !m.mouseInteractionsEnabled() {
		return nil
	}

	if m.menu != nil {
		return m.handleMenuMouse(mouse, true)
	}

	if mouse.Button == tea.MouseRight {
		return m.handleRowRightClick(mouse)
	}

	if mouse.Button != tea.MouseLeft {
		return nil
	}

	if m.moveSearchCursorToMouse(mouse) {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if row, ok := m.listRowAtMouse(mouse); ok {
		if !m.rowSelectable(row) {
			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		m.selectListRow(row)
		m.clearStatus()

		if handler := m.handleEnter(); handler != nil {
			return handler
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if handler, ok := m.handleFooterMouseClick(mouse); ok {
		return handler
	}

	return nil
}

func (m *Model) handleMouseWheel(mouse tea.Mouse) func() (tea.Model, tea.Cmd) {
	if !m.mouseInteractionsEnabled() || !m.pointInsidePopupBody(mouse) {
		return nil
	}

	switch mouse.Button {
	case tea.MouseWheelUp:
		if m.scrollListBy(-mouseWheelRows) {
			m.clearStatus()
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case tea.MouseWheelDown:
		if m.scrollListBy(mouseWheelRows) {
			m.clearStatus()
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case tea.MouseNone, tea.MouseLeft, tea.MouseMiddle, tea.MouseRight, tea.MouseWheelLeft, tea.MouseWheelRight,
		tea.MouseBackward, tea.MouseForward, tea.MouseButton10, tea.MouseButton11:
		return nil
	}

	return nil
}

func (m *Model) handleMouseMotion(mouse tea.Mouse) func() (tea.Model, tea.Cmd) {
	if !m.mouseInteractionsEnabled() {
		return nil
	}

	if m.menu != nil {
		return m.handleMenuMouse(mouse, false)
	}

	if m.updateFooterHover(mouse) {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	row, ok := m.listRowAtMouse(mouse)
	if !ok || !m.rowSelectable(row) {
		return nil
	}

	if !m.selectedListRow().equal(row) {
		m.selectListRow(row)
		m.clearStatus()
	}

	m.listViewportPolicy = listViewportPreserveTop

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) mouseInteractionsEnabled() bool {
	return m.popupStartup == popupStartupContent && m.width > 0 && m.height > 0 && !m.showHelp && m.dialog == dialogNone
}

func (m *Model) popupMouseLayout() popupMouseLayout {
	layout := m.calcPopupLayout(false)

	inset := 0
	if m.mode == modePopup {
		inset = popupViewportInset
	}

	inputY := inset + 1

	statusRows := 0

	listY := inputY + 1 + statusRows

	footerY := -1
	if layout.showFooter {
		footerY = listY + layout.listHeight
	}

	return popupMouseLayout{
		layout:     layout,
		bodyX:      inset + mouseBorderMargin,
		inputX:     inset + mouseBorderMargin + inputGutterWidth(),
		inputY:     inputY,
		inputWidth: max(layout.bodyWidth-inputGutterWidth()-1, 0),
		listY:      listY,
		footerY:    footerY,
		topY:       inset,
		bottomY:    inset + layout.bodyHeight + 1,
	}
}

func (m *Model) hasRenderedStatus() bool {
	return false
}

func (m *Model) pointInsidePopupBody(mouse tea.Mouse) bool {
	layout := m.popupMouseLayout()
	if mouse.X < layout.bodyX || mouse.X >= layout.bodyX+layout.layout.bodyWidth {
		return false
	}

	return mouse.Y > layout.topY && mouse.Y < layout.bottomY
}

func (m *Model) listRowAtMouse(mouse tea.Mouse) (popupListRow, bool) {
	layout := m.popupMouseLayout()
	if mouse.X < layout.bodyX || mouse.X >= layout.bodyX+layout.layout.bodyWidth {
		return emptyPopupListRow(), false
	}

	if mouse.Y < layout.listY || mouse.Y >= layout.listY+layout.layout.listHeight {
		return emptyPopupListRow(), false
	}

	rows := m.visibleListRows(layout.layout.listHeight)
	if len(rows) == 0 {
		return emptyPopupListRow(), false
	}

	rowIndex := mouse.Y - layout.listY
	if rowIndex < 0 || rowIndex >= len(rows) {
		return emptyPopupListRow(), false
	}

	return rows[rowIndex], true
}

func (m *Model) visibleSessionStart(height int) int {
	rows := m.popupListRows()
	if len(rows) == 0 || height <= 0 {
		return 0
	}

	return rows[m.visibleListStart(rows, height)].sessionIndex
}

func (m *Model) scrollListBy(delta int) bool {
	if len(m.filtered) == 0 || delta == 0 {
		return false
	}

	height := m.popupMouseLayout().layout.listHeight
	rows := m.popupListRows()
	previous := m.visibleListStart(rows, height)

	next := min(max(previous+delta, 0), max(len(rows)-height, 0))
	if next == previous {
		return false
	}

	m.listTop = next

	selected := selectedVisualIndex(rows, m.selectedListRow())
	if selected < next || selected >= next+height {
		if target, ok := m.scrollTargetRow(rows, next, height, delta); ok {
			m.selectListRow(target)
		}
	}

	m.listViewportPolicy = listViewportPreserveTop

	return true
}

func (m *Model) scrollTargetRow(
	rows []popupListRow,
	start int,
	height int,
	delta int,
) (popupListRow, bool) {
	end := min(start+height, len(rows))
	if delta > 0 {
		for _, row := range rows[start:end] {
			if m.rowSelectable(row) {
				return row, true
			}
		}
	} else {
		for index := end - 1; index >= start; index-- {
			if m.rowSelectable(rows[index]) {
				return rows[index], true
			}
		}
	}

	return emptyPopupListRow(), false
}

func (m *Model) moveSearchCursorToMouse(mouse tea.Mouse) bool {
	layout := m.popupMouseLayout()
	if layout.inputWidth <= 0 || mouse.Y != layout.inputY {
		return false
	}

	if mouse.X < layout.inputX || mouse.X >= layout.inputX+layout.inputWidth {
		return false
	}

	m.textInput.SetCursor(m.textInputCursorForColumn(mouse.X-layout.inputX, layout.inputWidth))

	return true
}

func (m *Model) textInputCursorForColumn(column int, width int) int {
	value := []rune(m.textInput.Value())
	if len(value) == 0 {
		return 0
	}

	start, end := visibleInputBounds(value, m.textInput.Position(), width)
	if column <= 0 {
		return start
	}

	visibleWidth := 0
	for i := start; i < end; i++ {
		visibleWidth += runeDisplayWidth(value[i])
		if visibleWidth > column {
			return i
		}
	}

	return end
}

func visibleInputBounds(value []rune, cursor int, width int) (int, int) {
	if width <= 0 || lipgloss.Width(string(value)) <= width {
		return 0, len(value)
	}

	cursor = min(max(cursor, 0), len(value))
	if cursor == 0 {
		return 0, visibleInputEnd(value, 0, width)
	}

	end := cursor
	start := visibleInputStart(value, end, width)

	return start, visibleInputEnd(value, start, width)
}

func visibleInputStart(value []rune, end int, width int) int {
	start := min(max(end, 0), len(value))

	visibleWidth := 0
	for start > 0 {
		nextWidth := visibleWidth + runeDisplayWidth(value[start-1])
		if nextWidth > width {
			break
		}

		visibleWidth = nextWidth
		start--
	}

	return start
}

func visibleInputEnd(value []rune, start int, width int) int {
	end := min(max(start, 0), len(value))

	visibleWidth := 0
	for end < len(value) {
		nextWidth := visibleWidth + runeDisplayWidth(value[end])
		if nextWidth > width {
			break
		}

		visibleWidth = nextWidth
		end++
	}

	if end == start && start < len(value) {
		end++
	}

	return end
}

func runeDisplayWidth(value rune) int {
	width := lipgloss.Width(string(value))
	if width <= 0 {
		return 1
	}

	return width
}

func (m *Model) handleFooterBadgeClick(mouse tea.Mouse, layout popupMouseLayout) (func() (tea.Model, tea.Cmd), bool) {
	if !layout.layout.showFooter || mouse.Y != layout.footerY {
		return nil, false
	}

	badgeX := layout.bodyX + max(layout.layout.bodyWidth-lipgloss.Width(m.renderFooterBadges()), 0)

	for _, badge := range m.footerBadges() {
		width := lipgloss.Width(badge.text)
		if badge.click != nil && mouse.X >= badgeX && mouse.X < badgeX+width {
			badge.click()

			return func() (tea.Model, tea.Cmd) { return m, nil }, true
		}

		badgeX += width + footerPartSpacing
	}

	return nil, false
}

func (m *Model) handleFooterMouseClick(mouse tea.Mouse) (func() (tea.Model, tea.Cmd), bool) {
	layout := m.popupMouseLayout()
	if handler, ok := m.handleFooterBadgeClick(mouse, layout); ok {
		return handler, true
	}

	if !layout.layout.showFooterHints || mouse.Y != layout.footerY {
		return nil, false
	}

	hintWidth := m.modeFooterHintWidth(layout.layout.bodyWidth)
	if mouse.X < layout.bodyX || mouse.X >= layout.bodyX+hintWidth {
		return nil, false
	}

	part, ok := m.footerPartAt(mouse.X-layout.bodyX, hintWidth)
	if !ok {
		return nil, false
	}

	action := m.resolveFooterAction(part)
	if action == nil {
		action = func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return action, true
}

func (m *Model) checkStatusBadgeHover(mouse tea.Mouse, layout popupMouseLayout) bool {
	if !m.hasStatusBadge() {
		return false
	}

	badgeX := layout.bodyX + max(layout.layout.bodyWidth-lipgloss.Width(m.renderFooterBadges()), 0)
	statusBadgeWidth := lipgloss.Width(m.renderStatusBadge())

	return mouse.X >= badgeX && mouse.X < badgeX+statusBadgeWidth
}

func (m *Model) updateFooterHover(mouse tea.Mouse) bool {
	previousDesc := m.footerHoverDesc
	previousStatusHover := m.statusBadgeHovered
	m.footerHoverDesc = ""
	m.statusBadgeHovered = false

	layout := m.popupMouseLayout()
	if !layout.layout.showFooter || mouse.Y != layout.footerY {
		return m.footerHoverDesc != previousDesc || m.statusBadgeHovered != previousStatusHover
	}

	m.statusBadgeHovered = m.checkStatusBadgeHover(mouse, layout)

	hintWidth := m.modeFooterHintWidth(layout.layout.bodyWidth)
	if layout.layout.showFooterHints && mouse.X >= layout.bodyX && mouse.X < layout.bodyX+hintWidth {
		if part, ok := m.footerPartAt(mouse.X-layout.bodyX, hintWidth); ok {
			m.footerHoverDesc = part.desc
		}
	}

	return m.footerHoverDesc != previousDesc || m.statusBadgeHovered != previousStatusHover
}

func (m *Model) footerPartAt(x int, width int) (footerPart, bool) {
	position := 0
	for _, part := range m.footerParts(width) {
		if x >= position && x < position+part.width && x < width {
			return part, true
		}

		position += part.width + footerPartSpacing
	}

	return emptyFooterPart(), false
}

func (m *Model) resolveFooterAction(part footerPart) func() (tea.Model, tea.Cmd) {
	if part.binding.Action != "" {
		return m.invokeAction(part.binding.Action, part.binding.Values)
	}

	switch part.desc {
	case "select":
		return m.handleEnter()
	case "cancel":
		return func() (tea.Model, tea.Cmd) { return m, tea.Quit }
	case "help":
		m.showHelp = true

		return func() (tea.Model, tea.Cmd) { return m, nil }
	default:
		return nil
	}
}

func emptyPopupListRow() popupListRow {
	var row popupListRow

	return row
}

func emptyFooterPart() footerPart {
	var part footerPart

	return part
}
