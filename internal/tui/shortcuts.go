package tui

import (
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

const (
	maxDirectShortcuts       = 10
	penultimateShortcutIndex = 8
	lastShortcutIndex        = 9
)

func (m *Model) handleActionKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	pressed := keyPressFromMsg(msg)

	keys := newActionKeyMap(m.config)
	if handler, matched := m.handleDisplayActionKeys(pressed, keys); matched {
		return handler
	}

	switch {
	case bubbleskey.Matches(pressed, keys.enter):
		return m.handleEnter()

	case m.matchesVisibleSessionShortcut(pressed, keys.selectKeys):
		return m.handleVisibleSessionShortcut(pressed, keys.selectKeys)

	case bubbleskey.Matches(pressed, keys.help):
		m.showHelp = true

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	for _, bound := range keys.actions {
		if bubbleskey.Matches(pressed, bound.key) {
			return m.invokeAction(bound.binding.Action, bound.binding.Values)
		}
	}

	return m.handleSessionKey(pressed)
}

func (m *Model) handleDisplayActionKeys(
	pressed keyPressSpec,
	keys actionKeyMap,
) (func() (tea.Model, tea.Cmd), bool) {
	switch {
	case bubbleskey.Matches(pressed, keys.cycleMode):
		if m.cycleDisplayMode() {
			m.clearStatus()
			return func() (tea.Model, tea.Cmd) { return m, m.saveDisplayStateCommand() }, true
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }, true
	case bubbleskey.Matches(pressed, keys.agentSort):
		if m.cycleAgentSort() {
			m.clearStatus()
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }, true
	default:
		return nil, false
	}
}

func (m *Model) saveDisplayStateCommand() tea.Cmd {
	ctx := m.operationContext()
	mode := string(m.displayMode)

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		err := m.updateState(ctx, func(current *session.State) {
			current.PopupDisplayMode = mode
		})

		return stateUpdateResultMsg{err: err}
	}
}

func (m *Model) matchesVisibleSessionShortcut(
	pressed keyPressSpec,
	bindings []bubbleskey.Binding,
) bool {
	_, ok := m.visibleSessionShortcutSlot(pressed, bindings)

	return ok
}

func (m *Model) handleVisibleSessionShortcut(
	pressed keyPressSpec,
	bindings []bubbleskey.Binding,
) func() (tea.Model, tea.Cmd) {
	slot, ok := m.visibleSessionShortcutSlot(pressed, bindings)
	if !ok {
		return nil
	}

	targets := m.visibleShortcutRows()
	if slot >= len(targets) {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.selectListRow(targets[slot])
	m.clearStatus()

	if handler := m.handleEnter(); handler != nil {
		return handler
	}

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) visibleSessionShortcutSlot(
	pressed keyPressSpec,
	bindings []bubbleskey.Binding,
) (int, bool) {
	for slot, binding := range bindings {
		if bubbleskey.Matches(pressed, binding) {
			return slot, true
		}
	}

	return 0, false
}

func (m *Model) visibleShortcutRows() []popupListRow {
	if m.shortcutRowsRendered {
		return m.shortcutRows
	}

	height := m.calcPopupLayout(m.hasRenderedStatus()).listHeight

	return m.collectShortcutRows(m.visibleListRows(height))
}

func (m *Model) collectShortcutRows(visible []popupListRow) []popupListRow {
	m.shortcutRows = m.shortcutRows[:0]
	for _, row := range visible {
		if m.shortcutTarget(row) {
			m.shortcutRows = append(m.shortcutRows, row)
		}
	}

	return m.shortcutRows
}

func (m *Model) shortcutTarget(row popupListRow) bool {
	if row.isSeparator() || row.sessionIndex < 0 || row.sessionIndex >= len(m.filtered) {
		return false
	}

	// In tree modes sessions are opened by their letter keys, so numbers go to agents only.
	return row.isAgent() || !m.displayMode.tree()
}

func (m *Model) visibleSessionShortcutLabel() string {
	bindings := newActionKeyMap(m.config).selectKeys

	labels := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if label := binding.Help().Key; label != "" {
			labels = append(labels, label)
		}
	}

	if len(labels) == maxDirectShortcuts {
		return labels[0] + ".." + labels[penultimateShortcutIndex] + "/" + labels[lastShortcutIndex]
	}

	return strings.Join(labels, "/")
}
