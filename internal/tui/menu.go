package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
)

const (
	menuBorderCells  = 2
	menuPaddingCells = 1
	menuSideCells    = 2 * menuPaddingCells
	menuKeyGap       = 3
)

var sessionMenuActions = map[string]action.ID{
	config.MenuRename:   action.SessionRename.ID,
	config.MenuSnapshot: action.SnapshotSave.ID,
	config.MenuKill:     action.SessionKill.ID,
	config.MenuDelete:   action.SessionDelete.ID,
}

var agentMenuActions = map[string]action.ID{
	config.MenuSnooze:   action.InboxSnooze.ID,
	config.MenuUnsnooze: action.InboxUnsnooze.ID,
	config.MenuSeen:     action.InboxSeen.ID,
	config.MenuUnseen:   action.InboxUnseen.ID,
	config.MenuPin:      action.InboxPin.ID,
	config.MenuUnpin:    action.InboxUnpin.ID,
	config.MenuEvidence: action.InboxEvidence.ID,
	config.MenuPR:       action.InboxPR.ID,
	config.MenuLink:     action.InboxLink.ID,
	config.MenuUnlink:   action.InboxUnlink.ID,
	config.MenuMerge:    action.TaskMerge.ID,
	config.MenuSleep:    action.TaskSleep.ID,
	config.MenuWake:     action.TaskWake.ID,
	config.MenuOpen:     action.TaskOpen.ID,
	config.MenuFinish:   action.InboxFinish.ID,
}

type rowMenu struct {
	title    string
	entries  []menuEntry
	cursor   int
	anchored bool
	anchorX  int
	anchorY  int
	box      menuBox
}

type menuEntry struct {
	label string
	key   string
	run   func() (tea.Model, tea.Cmd)
}

type menuBox struct {
	x, y, width, height int
}

func (entry menuEntry) divider() bool {
	return entry.run == nil
}

func (m *Model) openMenu(menu *rowMenu) func() (tea.Model, tea.Cmd) {
	entries := trimMenuDividers(menu.entries)
	if len(entries) == 0 {
		m.setErrorStatusf("No actions for %s", menu.title)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	menu.entries = entries
	menu.cursor = slices.IndexFunc(entries, func(entry menuEntry) bool { return !entry.divider() })
	m.menu = menu
	m.clearStatus()

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) closeMenu() {
	m.menu = nil
}

func trimMenuDividers(entries []menuEntry) []menuEntry {
	trimmed := make([]menuEntry, 0, len(entries))

	for _, entry := range entries {
		if entry.divider() && (len(trimmed) == 0 || trimmed[len(trimmed)-1].divider()) {
			continue
		}

		trimmed = append(trimmed, entry)
	}

	for len(trimmed) > 0 && trimmed[len(trimmed)-1].divider() {
		trimmed = trimmed[:len(trimmed)-1]
	}

	return trimmed
}

func (m *Model) handleMenuKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	if m.menu == nil {
		return nil
	}

	pressed := keyPressFromMsg(msg)

	switch pressed {
	case "esc", "ctrl+c", keyPressSpec(config.PickerKeyName(m.config.Keys.Menu)):
		m.closeMenu()

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "up", "ctrl+p":
		m.moveMenuCursor(-1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "down", "ctrl+n":
		m.moveMenuCursor(1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "enter":
		return m.runMenuEntry(m.menu.cursor)
	}

	for index, entry := range m.menu.entries {
		if !entry.divider() && entry.key != "" && keyPressSpec(config.PickerKeyName(entry.key)) == pressed {
			return m.runMenuEntry(index)
		}
	}

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) moveMenuCursor(direction int) {
	entries := m.menu.entries

	for step := 1; step <= len(entries); step++ {
		index := ((m.menu.cursor+direction*step)%len(entries) + len(entries)) % len(entries)
		if !entries[index].divider() {
			m.menu.cursor = index

			return
		}
	}
}

func (m *Model) runMenuEntry(index int) func() (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.menu.entries) || m.menu.entries[index].divider() {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	run := m.menu.entries[index].run
	m.closeMenu()

	return run
}

func (m *Model) handleMenuMouse(mouse tea.Mouse, click bool) func() (tea.Model, tea.Cmd) {
	if m.menu == nil {
		return nil
	}

	box := m.menu.box
	index := mouse.Y - box.y - 1
	inside := mouse.X >= box.x && mouse.X < box.x+box.width && index >= 0 && index < len(m.menu.entries)

	switch {
	case inside && click && mouse.Button == tea.MouseLeft:
		return m.runMenuEntry(index)
	case inside && !m.menu.entries[index].divider():
		m.menu.cursor = index
	case click:
		m.closeMenu()

		if mouse.Button == tea.MouseRight {
			return m.handleRowRightClick(mouse)
		}
	}

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) renderMenu(frame string) string {
	lines := m.menuLines()
	width := lipgloss.Width(lines[0])
	height := len(lines)
	contentWidth, contentHeight := m.contentWidth(), m.contentHeight()

	x, y := (contentWidth-width)/centerDivisor, (contentHeight-height)/centerDivisor
	if m.menu.anchored {
		x, y = m.menu.anchorX, m.menu.anchorY+1
		if y+height > contentHeight {
			y = max(m.menu.anchorY-height, 0)
		}
	}

	x = max(min(x, contentWidth-width), 0)
	y = max(min(y, contentHeight-height), 0)

	inset := 0
	if m.mode == modePopup {
		inset = popupViewportInset
	}

	m.menu.box = menuBox{x: x + inset, y: y + inset, width: width, height: height}

	base := strings.Split(frame, "\n")
	for len(base) < contentHeight {
		base = append(base, strings.Repeat(" ", contentWidth))
	}

	for index, line := range lines {
		if y+index < len(base) {
			base[y+index] = overlayLineAt(base[y+index], line, x, contentWidth)
		}
	}

	return strings.Join(base[:min(len(base), contentHeight)], "\n")
}

func (m *Model) menuLines() []string {
	labelWidth, keyWidth := lipgloss.Width(m.menu.title), 0

	for _, entry := range m.menu.entries {
		labelWidth = max(labelWidth, lipgloss.Width(entry.label))
		keyWidth = max(keyWidth, lipgloss.Width(entry.key))
	}

	inner := labelWidth + menuSideCells
	if keyWidth > 0 {
		inner += menuKeyGap + keyWidth
	}

	inner = min(inner, max(m.contentWidth()-menuBorderCells, 1))

	border := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Border))
	title := display.Truncate(" "+SanitizeTerminalText(m.menu.title)+" ", inner)
	top := border.Render("╭") + lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Primary)).Bold(true).Render(title) +
		border.Render(strings.Repeat("─", max(inner-lipgloss.Width(title), 0))+"╮")

	lines := []string{top}

	for index, entry := range m.menu.entries {
		if entry.divider() {
			lines = append(lines, border.Render("├"+strings.Repeat("─", inner)+"┤"))

			continue
		}

		lines = append(lines, border.Render("│")+m.menuEntryLine(entry, inner, labelWidth, index == m.menu.cursor)+border.Render("│"))
	}

	return append(lines, border.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

func (m *Model) menuEntryLine(entry menuEntry, inner, labelWidth int, selected bool) string {
	label := SanitizeTerminalText(entry.label)
	labelPart := strings.Repeat(" ", menuPaddingCells) + label + strings.Repeat(" ", max(labelWidth-lipgloss.Width(label), 0))

	keyPart := ""
	if entry.key != "" {
		keyPart = strings.Repeat(" ", menuKeyGap) + entry.key
	}

	padding := strings.Repeat(" ", max(inner-lipgloss.Width(labelPart+keyPart), 0))
	if lipgloss.Width(labelPart+keyPart) > inner {
		labelPart, keyPart, padding = display.Truncate(labelPart+keyPart, inner), "", ""
	}

	if selected {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color(m.config.Theme.Primary)).Bold(true).
			Render(labelPart + keyPart + padding)
	}

	return labelPart + lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted)).Render(keyPart) + padding
}

func (m *Model) openRowMenu() func() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.filtered) {
		return nil
	}

	selected := m.selectedListRow()
	if selected.isSeparator() || !m.rowSelectable(selected) {
		return nil
	}

	if selected.isAgent() {
		row := m.filtered[selected.sessionIndex]
		title := row.Agents[selected.agentIndex].HarnessSession

		return m.openMenu(&rowMenu{title: title, entries: m.agentMenuEntries(), cursor: 0, anchored: false, anchorX: 0, anchorY: 0, box: menuBox{x: 0, y: 0, width: 0, height: 0}})
	}

	row := m.filtered[selected.sessionIndex]

	return m.openMenu(&rowMenu{title: row.Name, entries: m.sessionMenuEntries(), cursor: 0, anchored: false, anchorX: 0, anchorY: 0, box: menuBox{x: 0, y: 0, width: 0, height: 0}})
}

func (m *Model) handleRowRightClick(mouse tea.Mouse) func() (tea.Model, tea.Cmd) {
	row, ok := m.listRowAtMouse(mouse)
	if !ok || !m.rowSelectable(row) {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.selectListRow(row)

	handler := m.openRowMenu()
	if handler == nil || m.menu == nil {
		return handler
	}

	inset := 0
	if m.mode == modePopup {
		inset = popupViewportInset
	}

	m.menu.anchored, m.menu.anchorX, m.menu.anchorY = true, max(mouse.X-inset, 0), max(mouse.Y-inset, 0)

	return handler
}

func (m *Model) sessionMenuEntries() []menuEntry {
	entries := make([]menuEntry, 0, len(m.config.Menu.Session))

	for _, name := range m.config.Menu.Session {
		switch name {
		case config.MenuDivider:
			entries = append(entries, menuEntry{label: "", key: "", run: nil})
		case config.MenuGo:
			if run := m.handleEnter(); run != nil {
				entries = append(entries, menuEntry{label: "go", key: "enter", run: run})
			}
		default:
			if entry, ok := m.actionMenuEntry(name, sessionMenuActions[name], action.Values{}); ok {
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

func (m *Model) agentMenuEntries() []menuEntry {
	items := m.selectedMenuItems()
	listed := make(map[string]bool, len(m.config.Menu.Agent))

	for _, name := range m.config.Menu.Agent {
		listed[name] = true
	}

	entries := make([]menuEntry, 0, len(m.config.Menu.Agent)+len(items))

	for _, name := range m.config.Menu.Agent {
		if name != config.MenuItems {
			entries = append(entries, m.agentMenuEntry(name, items)...)

			continue
		}

		for _, item := range slices.Sorted(maps.Keys(items)) {
			if !listed[item] {
				entries = append(entries, m.menuItemEntries(item, items[item])...)
			}
		}
	}

	return entries
}

func (m *Model) agentMenuEntry(name string, items map[string]config.MenuItemConfig) []menuEntry {
	switch {
	case name == config.MenuDivider:
		return []menuEntry{{label: "", key: "", run: nil}}
	case name == config.MenuGo:
		if run := m.handleEnter(); run != nil {
			return []menuEntry{{label: "go", key: "enter", run: run}}
		}
	case agentMenuActions[name] != "":
		id := agentMenuActions[name]
		if name == config.MenuMerge {
			if item, ok := m.selectedInboxItem(); ok && !m.unmergedItem(item) {
				return nil
			}

			if !m.selectedRowIsTask() {
				id = action.AgentMerge.ID
			}
		}

		if entry, ok := m.actionMenuEntry(name, id, action.Values{}); ok {
			return []menuEntry{entry}
		}
	default:
		if configured, ok := items[name]; ok {
			return m.menuItemEntries(name, configured)
		}
	}

	return nil
}

func (m *Model) menuItemEntries(name string, item config.MenuItemConfig) []menuEntry {
	entry, ok := m.actionMenuEntry(name, action.TaskRun.ID, action.Values{"action": {name}})
	if !ok {
		return nil
	}

	if item.Label != "" {
		entry.label = item.Label
	}

	if item.Key != "" {
		entry.key = config.PickerKeyName(item.Key)
	}

	return []menuEntry{entry}
}

func (m *Model) actionMenuEntry(label string, id action.ID, values action.Values) (menuEntry, bool) {
	entry, ok := action.Lookup(id)
	if !ok {
		return menuEntry{label: "", key: "", run: nil}, false
	}

	descriptor := entry.Describe()

	if descriptor.InPicker {
		if m.pickerActionError(id) != nil {
			return menuEntry{label: "", key: "", run: nil}, false
		}
	} else {
		subject, ok := m.actionSubject(descriptor)
		if !ok || m.actionAvailable(descriptor, subject) != nil {
			return menuEntry{label: "", key: "", run: nil}, false
		}

		if taskActionFitOf(id, m.selectedTaskLive()) == taskActionMisfits {
			return menuEntry{label: "", key: "", run: nil}, false
		}
	}

	key := ""

	for _, bound := range m.actionKeys().actions {
		if bound.binding.Action == id && maps.EqualFunc(bound.binding.Values, values, slices.Equal) {
			key = bound.key.Help().Key
		}
	}

	return menuEntry{label: label, key: key, run: func() (tea.Model, tea.Cmd) {
		if handler := m.invokeAction(id, values); handler != nil {
			return handler()
		}

		return m, nil
	}}, true
}

func (m *Model) selectedMenuItems() map[string]config.MenuItemConfig {
	item, ok := m.selectedInboxItem()
	if !ok || item.kind != inboxTaskRow {
		return nil
	}

	return m.config.MenuItemsFor(item.live.Record.Project)
}

func (m *Model) selectedRowIsTask() bool {
	item, ok := m.selectedInboxItem()

	return ok && item.kind == inboxTaskRow
}
