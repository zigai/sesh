package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestRegularArrowKeysSkipAgentSessionHeadings(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.normalizeFilteredCursor()

	steps := []struct {
		key  tea.KeyPressMsg
		want popupListRow
	}{
		{key: keyCode(tea.KeyDown), want: popupListRow{sessionIndex: 0, agentIndex: 0}},
		{key: keyCode(tea.KeyDown), want: popupListRow{sessionIndex: 0, agentIndex: 1}},
		{key: keyCtrl('n'), want: popupListRow{sessionIndex: 1, agentIndex: 0}},
		{key: keyCode(tea.KeyDown), want: popupListRow{sessionIndex: 2, agentIndex: 0}},
		{key: keyCode(tea.KeyDown), want: popupListRow{sessionIndex: 0, agentIndex: 0}},
		{key: keyCode(tea.KeyUp), want: popupListRow{sessionIndex: 2, agentIndex: 0}},
		{key: keyCode(tea.KeyUp), want: popupListRow{sessionIndex: 1, agentIndex: 0}},
	}

	for index, step := range steps {
		handler := model.handleNavigationKeys(step.key)
		if handler == nil {
			t.Fatalf("step %d key %q was not handled", index, step.key.String())
		}

		handler()

		if got := model.selectedListRow(); !got.equal(step.want) {
			t.Fatalf("step %d selection = %#v, want %#v", index, got, step.want)
		}
	}
}

func TestAgentCtrlArrowKeysJumpBetweenTmuxSessionGroups(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.cursor = 0
	model.agentCursor = 1

	steps := []struct {
		key  tea.KeyPressMsg
		want popupListRow
	}{
		{key: keyCtrlCode(tea.KeyDown), want: popupListRow{sessionIndex: 1, agentIndex: parentAgentIndex}},
		{key: keyCtrlCode(tea.KeyDown), want: popupListRow{sessionIndex: 2, agentIndex: parentAgentIndex}},
		{key: keyCtrlCode(tea.KeyDown), want: popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex}},
		{key: keyCtrlCode(tea.KeyUp), want: popupListRow{sessionIndex: 2, agentIndex: parentAgentIndex}},
	}

	for index, step := range steps {
		handler := model.handleNavigationKeys(step.key)
		if handler == nil {
			t.Fatalf("step %d key %q was not handled", index, step.key.String())
		}

		handler()

		if got := model.selectedListRow(); !got.equal(step.want) {
			t.Fatalf("step %d selection = %#v, want %#v", index, got, step.want)
		}
	}
}

func TestRegularArrowFromCtrlSelectedSessionEntersAgentRows(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.cursor = 0
	model.agentCursor = 1

	model.moveAgentGroup(1)
	model.moveSelectionByStep(1)

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 1, agentIndex: 0}); !got.equal(want) {
		t.Fatalf("selection after ctrl session then down = %#v, want first open agent %#v", got, want)
	}
}

func TestCtrlArrowGroupJumpKeepsHeadingWithFirstAgent(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.cursor = 0
	model.agentCursor = 1

	model.moveAgentGroup(1)
	visible := model.visibleListRows(2)

	want := []popupListRow{
		{sessionIndex: 1, agentIndex: parentAgentIndex},
		{sessionIndex: 1, agentIndex: 0},
	}
	if len(visible) != len(want) {
		t.Fatalf("visible rows = %#v, want heading and first agent %#v", visible, want)
	}

	for index := range want {
		if !visible[index].equal(want[index]) {
			t.Fatalf("visible row %d = %#v, want %#v", index, visible[index], want[index])
		}
	}
}

func TestAgentNavigationBoundariesSelectFirstAndLastAgents(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.normalizeFilteredCursor()

	model.selectListBoundary(true)

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 2, agentIndex: 0}); !got.equal(want) {
		t.Fatalf("bottom selection = %#v, want %#v", got, want)
	}

	model.selectListBoundary(false)

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 0, agentIndex: 0}); !got.equal(want) {
		t.Fatalf("top selection = %#v, want %#v", got, want)
	}
}

func TestAgentSelectionNormalizationPreservesHeadingSelection(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.cursor = 1
	model.agentCursor = parentAgentIndex

	model.normalizeFilteredCursor()

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 1, agentIndex: parentAgentIndex}); !got.equal(want) {
		t.Fatalf("normalized selection = %#v, want %#v", got, want)
	}
}

func TestCtrlArrowKeySelectsSessionWithOpenAgentsAndEnterSwitchesSession(t *testing.T) {
	model := newTestModel()
	switched := false
	dependenciesForTest(model).TmuxSwitchSession = func(context.Context, string) error {
		switched = true
		return nil
	}

	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.filtered = agentNavigationSessions()
	model.cursor = 0
	model.agentCursor = 0

	handler := model.handleNavigationKeys(keyCtrlCode(tea.KeyDown))
	if handler == nil {
		t.Fatal("ctrl+down was not handled")
	}

	handler()

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 1, agentIndex: parentAgentIndex}); !got.equal(want) {
		t.Fatalf("selection after ctrl+down = %#v, want session heading %#v", got, want)
	}

	enterFn := model.handleEnter()
	if enterFn == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := enterFn()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	model.Update(cmd())

	if model.DeferredSwitchSession() != "termtrace" || model.DeferredSwitchPane() != "" {
		t.Fatalf("enter target = %q/%q, want termtrace without pane", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}

	if switched {
		t.Fatal("popup switched sessions before quitting")
	}
}

func agentNavigationSessions() []session.Session {
	return []session.Session{
		{Name: "pi-codex-core", Agents: []session.AgentSession{{HarnessSession: "first"}, {HarnessSession: "second"}}},
		{Name: "termtrace", Agents: []session.AgentSession{{HarnessSession: "third"}}},
		{Name: "pi-tweaks", Agents: []session.AgentSession{{HarnessSession: "fourth"}}},
	}
}

func TestCtrlPQuitsWithFloatingTerminalReplayKey(t *testing.T) {
	model := newTestModel()

	handler := model.handleNavigationKeys(keyCtrl('p'))
	if handler == nil {
		t.Fatal("ctrl+p was not handled")
	}

	if _, cmd := handler(); cmd == nil {
		t.Fatal("ctrl+p returned no quit command")
	}

	if got := model.ReplayKey(); got != ReplayKeyFloatingTerminal {
		t.Fatalf("ReplayKey() = %q, want %q", got, ReplayKeyFloatingTerminal)
	}
}
