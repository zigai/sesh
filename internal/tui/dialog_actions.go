package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

const minDirSuggestions = 1

func (m *Model) handleDialogKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	switch m.dialog {
	case dialogConfirmKill:
		return m.handleConfirmActionKeys(msg, "Kill failed", tuiOperationKillSession)

	case dialogConfirmDelete:
		return m.handleConfirmActionKeys(msg, "Delete failed", tuiOperationDeleteSession)

	case dialogInputRename:
		return m.handleRenameDialogKeys(msg)

	case dialogInputCreateDir:
		return m.handleCreateDirDialogKeys(msg)

	case dialogNone:
		return nil
	}

	return nil
}

func (m *Model) handleConfirmActionKeys(
	msg tea.KeyPressMsg,
	errorPrefix string,
	operation tuiLogOperation,
) func() (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return m.runConfirmedAction(errorPrefix, operation)
	case "n", "N", "esc", "ctrl+c":
		m.dialog = dialogNone

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "left", "h":
		m.confirmYes = true

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "right", "l":
		m.confirmYes = false

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "tab", "shift+tab":
		m.confirmYes = !m.confirmYes

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "enter":
		if m.confirmYes {
			return m.runConfirmedAction(errorPrefix, operation)
		}

		m.dialog = dialogNone

		return func() (tea.Model, tea.Cmd) { return m, nil }
	default:
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}
}

func (m *Model) runConfirmedAction(
	errorPrefix string,
	operation tuiLogOperation,
) func() (tea.Model, tea.Cmd) {
	if m.confirmCommand != nil {
		command := m.confirmCommand
		m.confirmCommand = nil
		m.confirmAction = nil
		m.dialog = dialogNone
		m.clearStatus()

		return func() (tea.Model, tea.Cmd) { return m, command }
	}

	if m.confirmAction != nil {
		if err := m.confirmAction(); err != nil {
			logTUIFailure(m.operationContext(), operation, err)
			m.setErrorStatusf("%s: %v", errorPrefix, err)
			m.dialog = dialogNone

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		logTUIEvent(m.operationContext(), operation, tuiLogOutcomeSuccess)
	}

	m.dialog = dialogNone
	m.clearStatus()

	return func() (tea.Model, tea.Cmd) { return m, m.refreshSessions() }
}

func (m *Model) handleRenameDialogKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.submitRename()
	case "esc", "ctrl+c":
		m.dialog = dialogNone
		m.renameInput.SetValue("")
		m.renameInput.Blur()

		return func() (tea.Model, tea.Cmd) { return m, m.textInput.Focus() }
	default:
		var cmd tea.Cmd

		m.renameInput, cmd = m.renameInput.Update(msg)

		return func() (tea.Model, tea.Cmd) { return m, cmd }
	}
}

func (m *Model) submitRename() func() (tea.Model, tea.Cmd) {
	newName := strings.TrimSpace(m.renameInput.Value())
	if newName == "" {
		m.setErrorStatusf("Rename failed: new session name cannot be empty")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	target := m.renameTarget
	if target.Name == "" {
		if m.cursor >= len(m.filtered) {
			m.setErrorStatusf("Rename failed: no session selected")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		target = m.filtered[m.cursor]
	}

	if target.IsSaved() {
		m.setErrorStatusf("Rename unavailable for saved sessions")

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if target.Origin == session.OriginSSH {
		if m.deps.RemoteActions == nil {
			m.setErrorStatusf("Remote rename is not available: remote action service not configured")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		if !m.deps.RemoteActions.IsActionPermitted(m.config, target, "rename") {
			m.setErrorStatusf("Remote rename is disabled by remote permissions")

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}

		request := RemoteActionRequest{Action: "rename", NewName: newName, Directory: ""}

		m.clearStatus()
		m.dialog = dialogNone
		m.renameInput.SetValue("")
		m.renameInput.Blur()

		return func() (tea.Model, tea.Cmd) {
			return m, tea.Batch(m.textInput.Focus(), m.runRemoteAction(tuiOperationRenameSession, target, request))
		}
	}

	if m.deps.Renamer == nil {
		logTUIFailure(m.operationContext(), tuiOperationRenameSession, errRenameUnavailable)
		m.setErrorStatusf("Rename failed: %v", errRenameUnavailable)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	if err := m.deps.Renamer.RenameSession(m.operationContext(), target, newName); err != nil {
		logTUIFailure(m.operationContext(), tuiOperationRenameSession, err)
		m.setErrorStatusf("Rename failed: %v", err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	oldIdent := target.SemanticIdentity()
	newIdent := oldIdent
	newIdent.Name = newName
	_ = m.updateState(m.operationContext(), func(current *session.State) {
		current.RenameSessionIdentity(oldIdent, newIdent)
	})

	logTUIEvent(m.operationContext(), tuiOperationRenameSession, tuiLogOutcomeSuccess)

	m.clearStatus()

	m.dialog = dialogNone
	m.renameInput.SetValue("")
	m.renameInput.Blur()

	return func() (tea.Model, tea.Cmd) {
		return m, tea.Batch(m.textInput.Focus(), m.refreshSessions())
	}
}

func (m *Model) handleCreateDirDialogKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.applySelectedCreateDirSuggestion()
		return m.submitCreateDir()
	case "esc", "ctrl+c":
		m.cancelCreateDir()

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "tab":
		m.completeCreateDir(1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "shift+tab":
		m.completeCreateDir(-1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "down", "ctrl+n":
		m.moveCreateDirSuggestion(1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "up", "ctrl+p":
		m.moveCreateDirSuggestion(-1)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	case "ctrl+w", "alt+backspace":
		m.deleteCreateDirNodeBackward()

		return func() (tea.Model, tea.Cmd) { return m, nil }
	default:
		previous := m.createDirInput.Value()

		var cmd tea.Cmd

		m.createDirInput, cmd = m.createDirInput.Update(msg)
		if m.createDirInput.Value() != previous {
			m.refreshCreateDirSuggestions()
		}

		return func() (tea.Model, tea.Cmd) { return m, cmd }
	}
}

func (m *Model) submitCreateDir() func() (tea.Model, tea.Cmd) {
	sessionName := m.createSession
	dir := strings.TrimSpace(m.createDirInput.Value())

	m.dialog = dialogNone
	m.createSession = ""
	m.createDirSuggestions = nil
	m.createDirSuggestionIndex = 0
	m.createDirSuggestionTop = 0
	m.createDirInput.Blur()
	m.textInput.Focus()
	m.filterSessions()
	m.clearStatus()

	return func() (tea.Model, tea.Cmd) {
		return m, m.runSessionActionWithCreateDir(sessionName, dir)
	}
}

func (m *Model) cancelCreateDir() {
	sessionName := m.createSession

	m.dialog = dialogNone
	m.createSession = ""
	m.createDirSuggestions = nil
	m.createDirSuggestionIndex = 0
	m.createDirSuggestionTop = 0
	m.createDirInput.Blur()
	m.textInput.SetValue(sessionName)
	m.textInput.CursorEnd()
	m.textInput.Focus()
	m.filterSessions()
}

func (m *Model) completeCreateDir(direction int) {
	if len(m.createDirSuggestions) == 0 {
		return
	}

	if direction < 0 {
		m.moveCreateDirSuggestion(-1)
	}

	m.setCreateDirInputValue(m.createDirSuggestions[m.createDirSuggestionIndex])
	m.refreshCreateDirSuggestions()
}

func (m *Model) applySelectedCreateDirSuggestion() {
	if len(m.createDirSuggestions) == 0 || m.createDirSuggestionIndex < 0 || m.createDirSuggestionIndex >= len(m.createDirSuggestions) {
		return
	}

	m.setCreateDirInputValue(m.createDirSuggestions[m.createDirSuggestionIndex])
}

func (m *Model) moveCreateDirSuggestion(direction int) {
	if len(m.createDirSuggestions) == 0 || direction == 0 {
		return
	}

	m.createDirSuggestionIndex = (m.createDirSuggestionIndex + direction) % len(m.createDirSuggestions)
	if m.createDirSuggestionIndex < 0 {
		m.createDirSuggestionIndex = len(m.createDirSuggestions) - 1
	}

	m.scrollCreateDirSuggestions()
}

func (m *Model) deleteCreateDirNodeBackward() {
	value, cursor := deletePathNodeBackward(m.createDirInput.Value(), m.createDirInput.Position())
	m.createDirInput.SetValue(value)
	m.createDirInput.SetCursor(cursor)
	m.refreshCreateDirSuggestions()
}

func (m *Model) refreshCreateDirSuggestions() {
	cwd, err := m.deps.WorkingDirectory.GetWorkingDirectory()
	if err != nil {
		m.createDirSuggestions = nil
		m.createDirSuggestionIndex = 0

		return
	}

	suggestions, err := session.DirectoryPathSuggestions(m.operationContext(), m.createDirInput.Value(), cwd)
	if err != nil {
		if !errors.Is(err, session.ErrDirectoryLimit) {
			logTUIFailure(m.operationContext(), tuiOperationResolveWorkingDirectory, err)
		}

		m.createDirSuggestions = suggestions
		m.createDirSuggestionIndex = 0
		m.scrollCreateDirSuggestions()

		return
	}

	m.createDirSuggestions = suggestions
	m.createDirSuggestionIndex = 0
	m.scrollCreateDirSuggestions()
}

func (m *Model) setCreateDirInputValue(value string) {
	m.createDirInput.SetValue(value)
	m.createDirInput.CursorEnd()
}

func (m *Model) scrollCreateDirSuggestions() {
	if len(m.createDirSuggestions) == 0 {
		m.createDirSuggestionIndex = 0
		m.createDirSuggestionTop = 0

		return
	}

	if m.createDirSuggestionIndex < 0 {
		m.createDirSuggestionIndex = 0
	}

	if m.createDirSuggestionIndex >= len(m.createDirSuggestions) {
		m.createDirSuggestionIndex = len(m.createDirSuggestions) - 1
	}

	if m.createDirSuggestionTop > m.createDirSuggestionIndex {
		m.createDirSuggestionTop = m.createDirSuggestionIndex
	}

	visibleSuggestions := m.visibleDirSuggestions()
	if m.createDirSuggestionIndex >= m.createDirSuggestionTop+visibleSuggestions {
		m.createDirSuggestionTop = m.createDirSuggestionIndex - visibleSuggestions + 1
	}

	maxTop := max(len(m.createDirSuggestions)-visibleSuggestions, 0)
	if m.createDirSuggestionTop > maxTop {
		m.createDirSuggestionTop = maxTop
	}
}

func (m *Model) visibleDirSuggestions() int {
	return max(m.config.Settings.DirectorySuggestions, minDirSuggestions)
}
