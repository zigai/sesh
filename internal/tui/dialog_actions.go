package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

const minPathSuggestions = 1

const (
	pathUnknown pathState = iota
	pathExists
	pathMissing
	pathNotDirectory
)

type pathState uint8

func (m *Model) handleDialogKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	switch m.dialog {
	case dialogConfirm:
		return m.handleConfirmKeys(msg)

	case dialogInputText:
		return m.handleTextDialogInput(msg)

	case dialogInputPath:
		return m.handlePathDialogInput(msg)

	case dialogNone:
		return nil
	}

	return nil
}

func (m *Model) handleConfirmKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return m.confirmPending()
	case "n", "N", "esc", "ctrl+c":
		return m.cancelAction()
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
			return m.confirmPending()
		}

		return m.cancelAction()
	default:
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}
}

func (m *Model) handleTextDialogInput(msg tea.Msg) func() (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			return m.submitParam(m.textParamInput.Value())
		case "esc", "ctrl+c":
			return m.cancelAction()
		}
	}

	var cmd tea.Cmd

	m.textParamInput, cmd = m.textParamInput.Update(msg)

	return func() (tea.Model, tea.Cmd) { return m, cmd }
}

func (m *Model) handlePathDialogInput(msg tea.Msg) func() (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			m.applySelectedPathSuggestion()

			return m.submitParam(m.pathInput.Value())
		case "esc", "ctrl+c":
			return m.cancelAction()
		case "tab":
			m.completePath(1)

			return func() (tea.Model, tea.Cmd) { return m, nil }
		case "shift+tab":
			m.completePath(-1)

			return func() (tea.Model, tea.Cmd) { return m, nil }
		case "down", "ctrl+n":
			m.movePathSuggestion(1)

			return func() (tea.Model, tea.Cmd) { return m, nil }
		case "up", "ctrl+p":
			m.movePathSuggestion(-1)

			return func() (tea.Model, tea.Cmd) { return m, nil }
		case "ctrl+w", "alt+backspace":
			m.deletePathInputNodeBackward()

			return func() (tea.Model, tea.Cmd) { return m, nil }
		}
	}

	previous := m.pathInput.Value()

	var cmd tea.Cmd

	m.pathInput, cmd = m.pathInput.Update(msg)
	if m.pathInput.Value() != previous {
		m.refreshPathSuggestions()
	}

	return func() (tea.Model, tea.Cmd) { return m, cmd }
}

func (m *Model) completePath(direction int) {
	if len(m.pathSuggestions) == 0 {
		return
	}

	if direction < 0 {
		m.movePathSuggestion(-1)
	}

	m.setPathInputValue(m.pathSuggestions[m.pathSuggestionIndex])
	m.refreshPathSuggestions()
}

func (m *Model) applySelectedPathSuggestion() {
	if len(m.pathSuggestions) == 0 || m.pathSuggestionIndex < 0 || m.pathSuggestionIndex >= len(m.pathSuggestions) {
		return
	}

	m.setPathInputValue(m.pathSuggestions[m.pathSuggestionIndex])
}

func (m *Model) movePathSuggestion(direction int) {
	if len(m.pathSuggestions) == 0 || direction == 0 {
		return
	}

	m.pathSuggestionIndex = (m.pathSuggestionIndex + direction) % len(m.pathSuggestions)
	if m.pathSuggestionIndex < 0 {
		m.pathSuggestionIndex = len(m.pathSuggestions) - 1
	}

	m.scrollPathSuggestions()
}

func (m *Model) deletePathInputNodeBackward() {
	value, cursor := deletePathNodeBackward(m.pathInput.Value(), m.pathInput.Position())
	m.pathInput.SetValue(value)
	m.pathInput.SetCursor(cursor)
	m.refreshPathSuggestions()
}

func (m *Model) refreshPathSuggestions() {
	cwd, err := m.deps.WorkingDirectory.GetWorkingDirectory()
	if err != nil {
		m.pathSuggestions = nil
		m.pathSuggestionIndex = 0
		m.pathState = pathUnknown

		return
	}

	m.pathState = inspectPath(m.pathInput.Value(), cwd)

	suggestions, err := session.DirectoryPathSuggestions(m.operationContext(), m.pathInput.Value(), cwd)
	if err != nil {
		if !errors.Is(err, session.ErrDirectoryLimit) {
			logTUIFailure(m.operationContext(), tuiOperationResolveWorkingDirectory, err)
		}

		m.pathSuggestions = suggestions
		m.pathSuggestionIndex = 0
		m.scrollPathSuggestions()

		return
	}

	m.pathSuggestions = suggestions
	m.pathSuggestionIndex = 0
	m.scrollPathSuggestions()
}

func (m *Model) setPathInputValue(value string) {
	m.pathInput.SetValue(value)
	m.pathInput.CursorEnd()
}

func (m *Model) scrollPathSuggestions() {
	if len(m.pathSuggestions) == 0 {
		m.pathSuggestionIndex = 0
		m.pathSuggestionTop = 0

		return
	}

	if m.pathSuggestionIndex < 0 {
		m.pathSuggestionIndex = 0
	}

	if m.pathSuggestionIndex >= len(m.pathSuggestions) {
		m.pathSuggestionIndex = len(m.pathSuggestions) - 1
	}

	if m.pathSuggestionTop > m.pathSuggestionIndex {
		m.pathSuggestionTop = m.pathSuggestionIndex
	}

	visibleSuggestions := m.visiblePathSuggestions()
	if m.pathSuggestionIndex >= m.pathSuggestionTop+visibleSuggestions {
		m.pathSuggestionTop = m.pathSuggestionIndex - visibleSuggestions + 1
	}

	maxTop := max(len(m.pathSuggestions)-visibleSuggestions, 0)
	if m.pathSuggestionTop > maxTop {
		m.pathSuggestionTop = maxTop
	}
}

func (m *Model) visiblePathSuggestions() int {
	return max(m.config.Settings.DirectorySuggestions, minPathSuggestions)
}

func (m *Model) createsMissingPath(request pendingAction, value string) bool {
	flag := request.param.CreateWith
	if flag == "" || request.values.Has(flag) || request.subject.At.Kind != action.LocationLocal {
		return false
	}

	cwd, err := m.deps.WorkingDirectory.GetWorkingDirectory()
	if err != nil {
		return false
	}

	return inspectPath(value, cwd) == pathMissing
}

func inspectPath(value, cwd string) pathState {
	path := strings.TrimSpace(value)
	if path == "" {
		return pathUnknown
	}

	if expanded, ok := session.ExpandHomePath(path); ok {
		path = expanded
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}

	info, err := os.Stat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return pathMissing
	case err != nil:
		return pathUnknown
	case !info.IsDir():
		return pathNotDirectory
	default:
		return pathExists
	}
}
