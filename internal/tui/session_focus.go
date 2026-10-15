package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	agentKeysHintLength = 4
	keyChipForeground   = "0"
	fadedPercent        = 65
	focusCancelKey      = "esc"
	percentScale        = 100
)

type sessionKeySeqs struct {
	sessionKeySeq string
	keyChipSeq    string
	fadedSeq      string
}

func (m *Model) focusedSessionIndex() (int, bool) {
	if m.focusedSession == "" || !m.displayMode.tree() {
		return -1, false
	}

	index := slices.IndexFunc(m.filtered, func(row session.Session) bool { return row.RuntimeKey() == m.focusedSession })
	if index < 0 || len(m.filtered[index].Agents) == 0 {
		return -1, false
	}

	return index, true
}

func (m *Model) clearSessionFocus() {
	m.focusedSession = ""
}

func (m *Model) focusesOnSessionKey() bool {
	return m.displayMode.tree() && m.config.Settings.SessionKeyAction == "focus"
}

func (m *Model) agentKeys() string {
	return m.config.Keys.AgentKeys
}

func (m *Model) focusOrOpenSession(target session.Session) func() (tea.Model, tea.Cmd) {
	key := target.RuntimeKey()
	if !m.focusesOnSessionKey() || m.focusedSession == key {
		m.clearSessionFocus()

		return m.openSession(target)
	}

	if m.textInput.Value() != "" {
		m.textInput.SetValue("")
		m.filterSessions()
	}

	index := slices.IndexFunc(m.filtered, func(row session.Session) bool { return row.RuntimeKey() == key })
	if index < 0 || len(m.filtered[index].Agents) == 0 {
		m.clearSessionFocus()

		return m.openSession(target)
	}

	if len(m.filtered[index].Agents) == 1 {
		return m.openAgentRow(index, 0)
	}

	m.focusedSession = key
	m.selectListRow(popupListRow{sessionIndex: index, agentIndex: parentAgentIndex})

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) handleSessionFocusKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	index, ok := m.focusedSessionIndex()
	if !ok {
		m.clearSessionFocus()

		return nil
	}

	pressed := string(keyPressFromMsg(msg))
	if pressed == focusCancelKey {
		m.clearSessionFocus()

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	slot := strings.Index(m.agentKeys(), pressed)
	if len(pressed) != 1 || slot < 0 {
		return nil
	}

	if slot >= len(m.filtered[index].Agents) {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return m.openAgentRow(index, slot)
}

func (m *Model) openAgentRow(sessionIndex, agentIndex int) func() (tea.Model, tea.Cmd) {
	m.selectListRow(popupListRow{sessionIndex: sessionIndex, agentIndex: agentIndex})
	m.clearSessionFocus()

	if handler := m.handleEnter(); handler != nil {
		return handler
	}

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) focusedAgentKey(row session.Session, agent session.AgentSession) string {
	if _, ok := m.focusedSessionIndex(); !ok || row.RuntimeKey() != m.focusedSession {
		return ""
	}

	slot := slices.IndexFunc(row.Agents, func(candidate session.AgentSession) bool {
		return candidate.RegistryID == agent.RegistryID && candidate.PaneID == agent.PaneID
	})
	if slot < 0 || slot >= len(m.agentKeys()) {
		return ""
	}

	return m.agentKeys()[slot : slot+1]
}

func (m *Model) inFocusedSession(row popupListRow) bool {
	index, ok := m.focusedSessionIndex()

	return ok && !row.isSeparator() && row.sessionIndex == index
}

func (m *Model) fadesListRow(row popupListRow) bool {
	if m.sessionPrefixPending {
		return row.isAgent()
	}

	_, ok := m.focusedSessionIndex()

	return ok && !m.inFocusedSession(row)
}

func focusedRowStyles(styles sessionRowStyles, agent bool) sessionRowStyles {
	styles.fastDefaultTheme = false
	styles.selectedNameSeq = styles.nameSeq

	if !agent {
		styles.selectedNameSeq = ansiBold + styles.nameSeq
	}

	styles.selectedBgSeq = ""
	styles.selectedMuted = styles.mutedStyle
	styles.selectedBar = styles.blankPrefix
	styles.keyChipSeq = ansiBold + styles.sessionKeySeq

	return styles
}

func sessionKeyStyles(cfg *config.Config) sessionKeySeqs {
	theme := config.DefaultConfig().Theme
	if cfg != nil {
		theme = cfg.Theme
	}

	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Primary))

	return sessionKeySeqs{
		sessionKeySeq: extractStylePrefix(accent),
		keyChipSeq: extractStylePrefix(lipgloss.NewStyle().
			Foreground(lipgloss.Color(keyChipForeground)).
			Background(lipgloss.Color(shortcutKeyColor)).
			Bold(true)),
		fadedSeq: extractStylePrefix(lipgloss.NewStyle().Foreground(fadedColor(theme.Muted))),
	}
}

func fadedColor(muted string) color.Color {
	r, g, b, _ := lipgloss.Color(muted).RGBA()

	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", fadedChannel(r), fadedChannel(g), fadedChannel(b)))
}

func fadedChannel(value uint32) uint32 {
	return value >> 8 * fadedPercent / percentScale
}

func (m *Model) agentKeysHint() string {
	if len(m.agentKeys()) <= agentKeysHintLength {
		return m.agentKeys()
	}

	return m.agentKeys()[:agentKeysHintLength] + "…"
}

func (m *Model) sessionKeyStepFooterParts(keys actionKeyMap) []footerPart {
	if m.sessionPrefixPending {
		return []footerPart{
			newFooterPart("a-z", "session"),
			newFooterPart(focusCancelKey, "back"),
			newFooterPart(keys.help.Help().Key, "help"),
		}
	}

	if _, focused := m.focusedSessionIndex(); focused {
		return []footerPart{
			newFooterPart(m.agentKeysHint(), "agent"),
			newFooterPart(keys.enter.Help().Key, "open"),
			newFooterPart(focusCancelKey, "back"),
			newFooterPart(keys.help.Help().Key, "help"),
		}
	}

	return nil
}

func (m *Model) sessionKeyHelpBinding() helpBinding {
	hint := m.sessionKeyHint()
	if !m.config.Keys.SessionKeysHoldAlt() {
		hint += " key"
	}

	if m.focusesOnSessionKey() {
		return helpBinding{hint, "Focus session, then " + m.agentKeysHint() + " opens an agent and the key again opens it"}
	}

	return helpBinding{hint, "Open session by its key"}
}
