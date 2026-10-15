package tui

import (
	"cmp"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	sessionKeyLetters     = "abcdefghijklmnopqrstuvwxyz"
	sessionKeyAltHint     = "alt+key"
	sessionKeyGutterWidth = 2
)

type sessionKeysSavedMsg struct {
	keys []session.KeyAssignment
	err  error
}

type sessionKeyRules struct {
	overrides map[string]string
	blocked   map[string]bool
}

func newSessionKeyRules(cfg *config.Config) sessionKeyRules {
	blocked := make(map[string]bool)
	if cfg.Keys.SessionKeysHoldAlt() {
		blocked = altBlockedSessionKeys(cfg)
	}

	overrides := make(map[string]string, len(cfg.Keys.Sessions))
	for name, letter := range cfg.Keys.Sessions {
		overrides[name] = letter
		delete(blocked, letter)
	}

	return sessionKeyRules{overrides: overrides, blocked: blocked}
}

func altBlockedSessionKeys(cfg *config.Config) map[string]bool {
	keys := newActionKeyMap(cfg)
	reserved := config.PickerReservedKeys(cfg.Keys)

	blocked := make(map[string]bool)

	for _, letter := range sessionKeyLetters {
		key := cfg.Keys.PickerSessionKey(string(letter))

		taken := slices.ContainsFunc(keys.actions, func(bound boundAction) bool {
			return bubbleskey.Matches(keyPressSpec(key), bound.key)
		})
		if !taken {
			taken = slices.ContainsFunc(reserved, func(spec string) bool { return config.PickerKeyName(spec) == key })
		}

		if taken {
			blocked[string(letter)] = true
		}
	}

	return blocked
}

func localSessionIdentity(ident session.Identity) bool {
	return ident.Origin == "" || ident.Origin == string(session.OriginLocal)
}

func (rules sessionKeyRules) override(ident session.Identity) (string, bool) {
	if !localSessionIdentity(ident) {
		return "", false
	}

	letter, ok := rules.overrides[ident.Name]

	return letter, ok
}

func (rules sessionKeyRules) overrideLetter(letter string) bool {
	for _, overridden := range rules.overrides {
		if overridden == letter {
			return true
		}
	}

	return false
}

func assignSessionKeys(
	entries []session.KeyAssignment,
	listed []session.Identity,
	rules sessionKeyRules,
	reclaim bool,
) ([]session.KeyAssignment, bool) {
	present := make(map[session.Identity]bool, len(listed))
	for _, ident := range listed {
		present[ident.KeyIdentity()] = true
	}

	assigned, taken := keptSessionKeys(entries, rules)

	recorded := make(map[session.Identity]bool, len(assigned))
	for _, entry := range assigned {
		recorded[entry.Session] = true
	}

	unassigned := make([]session.Identity, 0, len(present))

	for ident := range present {
		if _, overridden := rules.override(ident); !overridden && !recorded[ident] {
			unassigned = append(unassigned, ident)
		}
	}

	slices.SortFunc(unassigned, compareKeyIdentities)

	for _, ident := range unassigned {
		letter, ok := freeSessionKey(ident.Name, taken, rules.blocked)

		// Unlisted sessions may live on another tmux server or an offline connection, so
		// their letters are taken back only once no letter is free.
		if !ok && reclaim {
			assigned, letter, ok = reclaimSessionKey(assigned, present)
		}

		if ok {
			assigned = append(assigned, session.KeyAssignment{Session: ident, Key: letter})
			taken[letter] = true
		}
	}

	return assigned, !slices.Equal(assigned, entries)
}

func keptSessionKeys(
	entries []session.KeyAssignment,
	rules sessionKeyRules,
) ([]session.KeyAssignment, map[string]bool) {
	kept := make([]session.KeyAssignment, 0, len(entries))
	taken := make(map[string]bool)

	for _, letter := range rules.overrides {
		taken[letter] = true
	}

	for _, entry := range entries {
		_, overridden := rules.override(entry.Session)
		if overridden || rules.blocked[entry.Key] || taken[entry.Key] {
			continue
		}

		kept = append(kept, entry)
		taken[entry.Key] = true
	}

	return kept, taken
}

func reclaimSessionKey(
	assigned []session.KeyAssignment,
	present map[session.Identity]bool,
) ([]session.KeyAssignment, string, bool) {
	index := slices.IndexFunc(assigned, func(entry session.KeyAssignment) bool { return !present[entry.Session] })
	if index < 0 {
		return assigned, "", false
	}

	letter := assigned[index].Key

	return slices.Delete(assigned, index, index+1), letter, true
}

func compareKeyIdentities(left, right session.Identity) int {
	return cmp.Or(
		cmp.Compare(left.Name, right.Name),
		cmp.Compare(left.Origin, right.Origin),
		cmp.Compare(left.ConnectionID, right.ConnectionID),
	)
}

func freeSessionKey(name string, taken, blocked map[string]bool) (string, bool) {
	for _, candidate := range strings.ToLower(name) + sessionKeyLetters {
		letter := string(candidate)
		if config.ValidSessionKey(letter) && !taken[letter] && !blocked[letter] {
			return letter, true
		}
	}

	return "", false
}

func (m *Model) sessionKey(row session.Session) string {
	ident := row.SemanticIdentity().KeyIdentity()

	if letter, ok := m.sessionKeys.override(ident); ok {
		return letter
	}

	letter, ok := m.state.SessionKey(ident)
	if !ok || m.sessionKeys.blocked[letter] || m.sessionKeys.overrideLetter(letter) {
		return ""
	}

	return letter
}

func (m *Model) showsSessionKeys() bool {
	return m.shortcutStyle() == "indent" || m.sessionPrefixPending
}

func (m *Model) sessionKeyHint() string {
	if m.config.Keys.SessionKeysHoldAlt() {
		return sessionKeyAltHint
	}

	return config.PickerKeyName(m.config.Keys.SessionPrefix)
}

func (m *Model) sessionKeyFooterPart() footerPart {
	if m.focusesOnSessionKey() {
		return newFooterPart(m.sessionKeyHint(), "focus")
	}

	return newFooterPart(m.sessionKeyHint(), "open")
}

func (m *Model) withoutSessionKeyHintWhenTooWide(parts []footerPart, width int) []footerPart {
	if footerPartsWidth(parts) <= width {
		return parts
	}

	hint := m.sessionKeyHint()

	return slices.DeleteFunc(parts, func(part footerPart) bool { return part.key == hint })
}

func (m *Model) sessionKeyGutter() string {
	if m.shortcutStyle() != "indent" {
		return ""
	}

	return strings.Repeat(" ", sessionKeyGutterWidth)
}

func (m *Model) rowKey(row session.Session, agent session.AgentSession, hasAgent bool) string {
	if hasAgent {
		if m.shortcutStyle() == "right" {
			return ""
		}

		return m.focusedAgentKey(row, agent)
	}

	if m.sessionPrefixPending || (m.config.Keys.SessionKeysHoldAlt() && m.shortcutStyle() == "indent") {
		return m.sessionKey(row)
	}

	return ""
}

func rowPrefix(styles sessionRowStyles, key string, selected bool) string {
	switch {
	case key == "" && selected:
		return styles.selectedBar
	case key == "":
		return styles.blankPrefix
	case selected:
		return styles.keyChipSeq + key + ansiReset + styles.selectedBgSeq + " " + ansiReset
	default:
		return ansiBold + styles.sessionKeySeq + key + ansiReset + " "
	}
}

func (m *Model) sessionForLetter(letter string) (session.Session, bool) {
	var found session.Session

	if !config.ValidSessionKey(letter) {
		return found, false
	}

	matched := false

	for _, row := range m.sessions {
		if m.sessionKey(row) != letter {
			continue
		}

		if !row.Saved() {
			return row, true
		}

		found, matched = row, true
	}

	return found, matched
}

func (m *Model) handleSessionKey(pressed keyPressSpec) func() (tea.Model, tea.Cmd) {
	letter, ok := strings.CutPrefix(string(pressed), "alt+")
	if !ok || !m.config.Keys.SessionKeysHoldAlt() {
		return nil
	}

	target, ok := m.sessionForLetter(letter)
	if !ok {
		return nil
	}

	m.clearStatus()

	return m.focusOrOpenSession(target)
}

func (m *Model) handleSessionPrefixKeys(msg tea.KeyPressMsg) func() (tea.Model, tea.Cmd) {
	if m.config.Keys.SessionKeysHoldAlt() {
		return nil
	}

	pressed := string(keyPressFromMsg(msg))
	stay := func() (tea.Model, tea.Cmd) { return m, nil }

	if !m.sessionPrefixPending {
		if config.PickerKeyName(pressed) != config.PickerKeyName(m.config.Keys.SessionPrefix) {
			return nil
		}

		m.sessionPrefixPending = true

		return stay
	}

	if pressed == focusCancelKey || config.PickerKeyName(pressed) == config.PickerKeyName(m.config.Keys.SessionPrefix) {
		m.sessionPrefixPending = false

		return stay
	}

	if !config.ValidSessionKey(pressed) {
		m.sessionPrefixPending = false

		return nil
	}

	target, ok := m.sessionForLetter(pressed)
	if !ok {
		return stay
	}

	m.sessionPrefixPending = false
	m.clearStatus()

	return m.focusOrOpenSession(target)
}

func sessionKeyIdentities(rows []session.Session) []session.Identity {
	idents := make([]session.Identity, 0, len(rows))
	for _, row := range rows {
		if row.Name != "" {
			idents = append(idents, row.SemanticIdentity().KeyIdentity())
		}
	}

	return idents
}

func (m *Model) assignLoadedSessionKeys(reclaim bool) tea.Cmd {
	listed := sessionKeyIdentities(m.sessions)
	rules := m.sessionKeys

	entries, changed := assignSessionKeys(m.state.SessionKeys, listed, rules, reclaim)
	if !changed {
		return nil
	}

	m.state.SessionKeys = entries
	ctx := m.operationContext()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		var saved []session.KeyAssignment

		err := m.deps.State.UpdateModelState(ctx, func(current *session.State) {
			current.SessionKeys, _ = assignSessionKeys(current.SessionKeys, listed, rules, reclaim)
			saved = slices.Clone(current.SessionKeys)
		})

		return sessionKeysSavedMsg{keys: saved, err: err}
	}
}

func (m *Model) handleSessionKeysSaved(msg sessionKeysSavedMsg) {
	if msg.err != nil {
		logTUIFailure(m.operationContext(), tuiOperationSaveState, msg.err)
		m.setErrorStatusf("Failed to save state: %v", msg.err)

		return
	}

	m.state.SessionKeys = msg.keys
}
