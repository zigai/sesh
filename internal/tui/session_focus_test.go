package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/session"
)

func focusTestModel() *Model {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.displayModes = []displayMode{displayModeSesh, displayModeAgents}
	model.width = 80
	model.height = 14
	model.state.SessionKeys = []session.KeyAssignment{
		{Session: session.Identity{Name: testSupportSessionDevName}, Key: "d"},
		{Session: session.Identity{Name: "site"}, Key: "i"},
		{Session: session.Identity{Name: "notes"}, Key: "n"},
		{Session: session.Identity{Name: "api"}, Key: "p"},
	}
	model.sessions = []session.Session{
		{Name: testSupportSessionDevName, Kind: session.KindLive, Agents: []session.AgentSession{
			{Harness: "pi", PaneID: "%1", RegistryID: "dev-1"},
			{Harness: "pi", PaneID: "%2", RegistryID: "dev-2"},
		}},
		{Name: "site", Kind: session.KindLive, Agents: []session.AgentSession{
			{Harness: "pi", PaneID: "%3", RegistryID: "site-1"},
		}},
		{Name: "notes", Kind: session.KindLive},
		{Name: "api", Kind: session.KindLive, Agents: []session.AgentSession{
			{Harness: "pi", PaneID: "%4", RegistryID: "api-1"},
			{Harness: "pi", PaneID: "%5", RegistryID: "api-2"},
		}},
	}
	model.displaySessions = model.sessions
	model.filtered = model.sessions

	return model
}

func pressKey(model *Model, msg tea.KeyPressMsg) tea.Cmd {
	_, cmd := model.Update(msg)

	return cmd
}

func pressAndRun(model *Model, msg tea.KeyPressMsg) {
	if cmd := pressKey(model, msg); cmd != nil {
		model.Update(cmd())
	}
}

func pressSessionKey(model *Model, letter string) tea.Cmd {
	pressKey(model, keyCode(tea.KeyTab))

	return pressKey(model, keyText(letter))
}

func runSessionKey(model *Model, letter string) {
	if cmd := pressSessionKey(model, letter); cmd != nil {
		model.Update(cmd())
	}
}

func holdAltForSessionKeys(model *Model) {
	model.config.Keys.SessionPrefix = "alt"
	model.sessionKeys = newSessionKeyRules(model.config, model.actionBindings)
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}

	_, ok := cmd().(tea.QuitMsg)

	return ok
}

func TestSessionKeyFocusesSessionWithAgents(t *testing.T) {
	model := focusTestModel()

	if cmd := pressSessionKey(model, "d"); isQuit(cmd) {
		t.Fatal("tab d closed the picker instead of focusing dev")
	}

	if model.focusedSession != model.filtered[0].RuntimeKey() {
		t.Fatalf("focused session = %q, want dev", model.focusedSession)
	}

	if model.cursor != 0 || model.agentCursor != parentAgentIndex {
		t.Fatalf("selection = %d/%d, want the dev session row", model.cursor, model.agentCursor)
	}

	if model.DeferredSwitchSession() != "" || model.textInput.Value() != "" {
		t.Fatalf("tab d switched to %q or searched %q", model.DeferredSwitchSession(), model.textInput.Value())
	}
}

func TestFocusedKeyOpensThatAgent(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	pressAndRun(model, keyText("s"))

	if model.DeferredSwitchSession() != testSupportSessionDevName || model.DeferredSwitchPane() != "%2" {
		t.Fatalf("switch target = %q/%q, want dev pane %%2", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestFocusedKeyBeyondAgentsDoesNothing(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	pressAndRun(model, keyText("d"))

	if model.DeferredSwitchSession() != "" {
		t.Fatalf("key beyond the agent count switched to %q", model.DeferredSwitchSession())
	}

	if model.textInput.Value() != "" {
		t.Fatalf("key beyond the agent count typed %q into search", model.textInput.Value())
	}

	if model.focusedSession != model.filtered[0].RuntimeKey() {
		t.Fatal("key beyond the agent count dropped the focus")
	}
}

func TestLetterWithoutSessionPrefixSearches(t *testing.T) {
	model := focusTestModel()

	pressKey(model, keyText("d"))

	if model.textInput.Value() != "d" || model.DeferredSwitchSession() != "" || model.focusedSession != "" {
		t.Fatalf("d without tab: search = %q, switch = %q, focus = %q; want search d only", model.textInput.Value(), model.DeferredSwitchSession(), model.focusedSession)
	}
}

func TestSessionKeyOpensSessionWithoutAgents(t *testing.T) {
	model := focusTestModel()

	runSessionKey(model, "n")

	if model.DeferredSwitchSession() != "notes" {
		t.Fatalf("switch target = %q, want notes", model.DeferredSwitchSession())
	}
}

func TestSessionKeyOpensTheOnlyAgent(t *testing.T) {
	model := focusTestModel()

	runSessionKey(model, "i")

	if model.DeferredSwitchSession() != "site" || model.DeferredSwitchPane() != "%3" {
		t.Fatalf("switch target = %q/%q, want site pane %%3", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestSessionKeyAgainOpensFocusedSession(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	runSessionKey(model, "d")

	if model.DeferredSwitchSession() != testSupportSessionDevName || model.DeferredSwitchPane() != "" {
		t.Fatalf("switch target = %q/%q, want the dev session", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestOtherSessionKeyMovesFocus(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	pressSessionKey(model, "p")

	if model.focusedSession != model.filtered[3].RuntimeKey() || model.cursor != 3 {
		t.Fatalf("focus = %q at cursor %d, want api", model.focusedSession, model.cursor)
	}

	if model.DeferredSwitchSession() != "" {
		t.Fatalf("moving focus switched to %q", model.DeferredSwitchSession())
	}
}

func TestUnassignedLetterAfterPrefixKeepsWaiting(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "z")

	if !model.sessionPrefixPending || model.textInput.Value() != "" {
		t.Fatalf("tab z: waiting = %v, search = %q; want still waiting and no search", model.sessionPrefixPending, model.textInput.Value())
	}

	pressKey(model, keyText("d"))

	if model.focusedSession != model.filtered[0].RuntimeKey() {
		t.Fatalf("focus after tab z d = %q, want dev", model.focusedSession)
	}
}

func TestEscStepsBackBeforeClosing(t *testing.T) {
	model := focusTestModel()

	pressKey(model, keyCode(tea.KeyTab))

	if cmd := pressKey(model, keyCode(tea.KeyEscape)); isQuit(cmd) || model.sessionPrefixPending {
		t.Fatal("esc after tab closed the picker or kept waiting for a session letter")
	}

	pressSessionKey(model, "d")

	if cmd := pressKey(model, keyCode(tea.KeyEscape)); isQuit(cmd) || model.focusedSession != "" {
		t.Fatal("esc with a focused session closed the picker or kept the focus")
	}

	if cmd := pressKey(model, keyCode(tea.KeyEscape)); !isQuit(cmd) {
		t.Fatal("esc without focus did not close the picker")
	}
}

func TestOtherKeyAfterPrefixStopsWaitingAndActs(t *testing.T) {
	model := focusTestModel()

	pressKey(model, keyCode(tea.KeyTab))
	pressKey(model, keyCode(tea.KeyDown))

	if model.sessionPrefixPending {
		t.Fatal("down after tab kept waiting for a session letter")
	}

	if model.cursor != 0 || model.agentCursor != 0 {
		t.Fatalf("down after tab left the selection at %d/%d, want dev's first agent", model.cursor, model.agentCursor)
	}
}

func TestArrowsKeepFocusAndOtherTypingClearsIt(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	pressKey(model, keyCode(tea.KeyDown))

	if model.focusedSession == "" {
		t.Fatal("arrow key dropped the focus")
	}

	pressKey(model, keyText("1"))

	if model.focusedSession != "" || model.textInput.Value() != "1" {
		t.Fatalf("typing: focus = %q, search = %q; want no focus and search 1", model.focusedSession, model.textInput.Value())
	}
}

func TestSessionKeyClearsSearchHidingTheSession(t *testing.T) {
	model := focusTestModel()

	pressKey(model, keyText("s"))
	pressKey(model, keyText("i"))
	pressKey(model, keyText("t"))
	pressSessionKey(model, "d")

	if model.textInput.Value() != "" {
		t.Fatalf("search = %q, want it cleared to show dev", model.textInput.Value())
	}

	index, ok := model.focusedSessionIndex()
	if !ok || model.filtered[index].Name != testSupportSessionDevName {
		t.Fatalf("focused row = %d/%v, want dev", index, ok)
	}
}

func TestFocusEndsWhenFocusedSessionDisappears(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")

	model.displaySessions = model.sessions[1:]
	model.filtered = model.displaySessions

	pressAndRun(model, keyText("a"))

	if model.DeferredSwitchSession() != "" || model.textInput.Value() != "a" {
		t.Fatalf("agent key after the focused session vanished: switch = %q, search = %q", model.DeferredSwitchSession(), model.textInput.Value())
	}
}

func TestModeSwitchClearsFocus(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")
	model.cycleDisplayMode()

	if model.focusedSession != "" {
		t.Fatal("mode switch kept the focus")
	}
}

func TestAltSessionKeysWhenPrefixIsAlt(t *testing.T) {
	model := focusTestModel()
	holdAltForSessionKeys(model)

	pressKey(model, keyCode(tea.KeyTab))
	pressKey(model, keyText("d"))

	if model.focusedSession != "" || model.textInput.Value() != "d" {
		t.Fatalf("tab d with the alt prefix: focus = %q, search = %q; want search d", model.focusedSession, model.textInput.Value())
	}

	model.textInput.SetValue("")
	model.filterSessions()
	pressKey(model, keyAlt("d"))

	if model.focusedSession != model.filtered[0].RuntimeKey() {
		t.Fatalf("alt+d focused %q, want dev", model.focusedSession)
	}
}

func TestAltSessionKeysDoNothingWithTabPrefix(t *testing.T) {
	model := focusTestModel()

	pressAndRun(model, keyAlt("n"))

	if model.DeferredSwitchSession() != "" {
		t.Fatalf("alt+n with the tab prefix switched to %q", model.DeferredSwitchSession())
	}
}

func TestConfiguredAgentKeysOpenAgents(t *testing.T) {
	model := focusTestModel()
	model.config.Keys.AgentKeys = "123"

	pressSessionKey(model, "d")
	pressAndRun(model, keyText("2"))

	if model.DeferredSwitchSession() != testSupportSessionDevName || model.DeferredSwitchPane() != "%2" {
		t.Fatalf("switch target = %q/%q, want dev pane %%2", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestOpenActionOpensSessionDirectly(t *testing.T) {
	model := focusTestModel()
	model.config.Settings.SessionKeyAction = "open"

	runSessionKey(model, "d")

	if model.DeferredSwitchSession() != testSupportSessionDevName || model.DeferredSwitchPane() != "" {
		t.Fatalf("switch target = %q/%q, want the dev session", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestSessionKeyOpensSessionInSeshMode(t *testing.T) {
	model := focusTestModel()
	model.displayMode = displayModeSesh

	runSessionKey(model, "d")

	if model.DeferredSwitchSession() != testSupportSessionDevName {
		t.Fatalf("switch target = %q, want dev", model.DeferredSwitchSession())
	}
}

func TestFocusedAgentKeysRunOutAfterTheBottomRow(t *testing.T) {
	model := focusTestModel()

	agents := make([]session.AgentSession, 27)
	for index := range agents {
		agents[index] = session.AgentSession{PaneID: "%" + strconv.Itoa(index)}
	}

	model.filtered[0].Agents = agents
	model.focusedSession = model.filtered[0].RuntimeKey()

	if got := model.focusedAgentKey(model.filtered[0], agents[9]); got != "q" {
		t.Fatalf("tenth agent key = %q, want q", got)
	}

	if got := model.focusedAgentKey(model.filtered[0], agents[25]); got != "m" {
		t.Fatalf("26th agent key = %q, want m", got)
	}

	if got := model.focusedAgentKey(model.filtered[0], agents[26]); got != "" {
		t.Fatalf("27th agent key = %q, want none", got)
	}
}

func TestFocusFadesOtherRowsWithoutMovingThem(t *testing.T) {
	model := focusTestModel()

	before := strings.Split(model.renderList(60, 7), "\n")

	pressSessionKey(model, "d")

	after := strings.Split(model.renderList(60, 7), "\n")
	if len(before) != len(after) {
		t.Fatalf("focus changed the row count from %d to %d", len(before), len(after))
	}

	for index := range before {
		if lipgloss.Width(before[index]) != lipgloss.Width(after[index]) {
			t.Fatalf("row %d width changed from %d to %d", index, lipgloss.Width(before[index]), lipgloss.Width(after[index]))
		}
	}

	if firstField(after[1]) != "a" || firstField(after[2]) != "s" {
		t.Fatalf("focused agents are not keyed a and s: %q / %q", stripANSI(after[1]), stripANSI(after[2]))
	}

	siteRow := 3
	if stripANSI(before[siteRow]) != stripANSI(after[siteRow]) {
		t.Fatalf("fading changed the text of an unfocused row: %q -> %q", stripANSI(before[siteRow]), stripANSI(after[siteRow]))
	}

	if before[siteRow] == after[siteRow] {
		t.Fatalf("unfocused row was not faded: %q", after[siteRow])
	}
}

func TestFocusedFooterNamesTheAgentKeys(t *testing.T) {
	model := focusTestModel()

	pressSessionKey(model, "d")

	footer := stripANSI(model.renderFooter(model.width))
	for _, want := range []string{"asdf… agent", "esc back"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer %q is missing %q", footer, want)
		}
	}
}

func firstField(row string) string {
	fields := strings.Fields(stripANSI(row))
	if len(fields) == 0 {
		return ""
	}

	return fields[0]
}

func TestSessionPrefixShowsHiddenSessionLetters(t *testing.T) {
	model := focusTestModel()

	if row := stripANSI(strings.Split(model.renderList(60, 7), "\n")[0]); strings.HasPrefix(row, "▌ d ") {
		t.Fatalf("letters shown before tab with shortcut_style none: %q", row)
	}

	pressKey(model, keyCode(tea.KeyTab))

	if row := stripANSI(strings.Split(model.renderList(60, 7), "\n")[0]); !strings.HasPrefix(row, "d dev") {
		t.Fatalf("row after tab = %q, want the d letter in the first column", row)
	}
}

func TestSessionPrefixFadesAgentRowsOnly(t *testing.T) {
	model := focusTestModel()
	model.config.Settings.ShortcutStyle = "indent"

	before := strings.Split(model.renderList(60, 7), "\n")

	pressKey(model, keyCode(tea.KeyTab))

	after := strings.Split(model.renderList(60, 7), "\n")
	if len(before) != len(after) {
		t.Fatalf("tab changed the row count from %d to %d", len(before), len(after))
	}

	for index := range before {
		if lipgloss.Width(before[index]) != lipgloss.Width(after[index]) {
			t.Fatalf("tab changed the width of row %d", index)
		}
	}

	agentRow, sessionRow := 1, 3
	if stripANSI(before[agentRow]) != stripANSI(after[agentRow]) || before[agentRow] == after[agentRow] {
		t.Fatalf("agent row was not faded in place after tab: %q", after[agentRow])
	}

	if !strings.HasPrefix(stripANSI(after[sessionRow]), "i   site") || strings.Contains(after[sessionRow], model.sessionRowStyles(model.calcSessionLayout(60)).fadedSeq) {
		t.Fatalf("session row after tab = %q, want its letter shown and not faded", stripANSI(after[sessionRow]))
	}
}

func TestCursorRowDrawsItsKeyAsTheSelectionMarker(t *testing.T) {
	model := focusTestModel()
	chip := model.sessionRowStyles(model.calcSessionLayout(60)).keyChipSeq

	pressKey(model, keyCode(tea.KeyTab))

	rows := strings.Split(model.renderList(60, 7), "\n")
	if !strings.HasPrefix(rows[0], chip+"d") {
		t.Fatalf("cursor row after tab = %q, want its d key drawn as the selection marker", rows[0])
	}

	if strings.HasPrefix(rows[3], chip) {
		t.Fatalf("row without the cursor = %q, drew the selection marker", rows[3])
	}
}
