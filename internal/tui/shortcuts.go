package tui

import (
	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func (m *Model) handleActionKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	pressed := keyPressFromMsg(msg)

	keys := m.actionKeys()
	if handler, matched := m.handleDisplayActionKeys(pressed, keys); matched {
		return handler
	}

	switch {
	case bubbleskey.Matches(pressed, keys.enter):
		return m.handleEnter()

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
	case bubbleskey.Matches(pressed, keys.inboxGroup) && m.displayMode == displayModeInbox:
		if m.cycleInboxGroup() {
			m.clearStatus()
		}

		return func() (tea.Model, tea.Cmd) { return m, nil }, true
	case bubbleskey.Matches(pressed, keys.menu):
		if handler := m.openRowMenu(); handler != nil {
			m.anchorMenuAtSelection()

			return handler, true
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
