package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestVisibleSessionShortcutSwitchesVisibleSlot(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 8
	model.cursor = 7
	model.filtered = testSessions(12)

	dependenciesForTest(model).TmuxSessionExists = func(_ context.Context, name string) (bool, error) {
		return true, nil
	}
	dependenciesForTest(model).TmuxIsInsideTmux = func() bool {
		return true
	}

	switched := ""
	dependenciesForTest(model).TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}

	cmdFn := model.handleActionKeys(keyCtrl('2'))
	if cmdFn == nil {
		t.Fatal("handleActionKeys(ctrl+2) returned nil")
	}

	if got, want := model.cursor, 5; got != want {
		t.Fatalf("cursor after ctrl+2 = %d, want %d", got, want)
	}

	_, cmd := cmdFn()
	if cmd == nil {
		t.Fatal("visible session shortcut returned nil command")
	}

	_, quit := model.Update(cmd())
	if quit == nil {
		t.Fatal("shortcut action did not quit")
	}

	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("shortcut action did not request program exit")
	}

	if switched != "session-5" {
		t.Fatalf("switched session = %q, want session-5", switched)
	}
}

func TestVisibleSessionShortcutZeroSwitchesTenthVisibleSlot(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 16
	model.filtered = testSessions(12)
	model.config.Keys.Select0 = "x"

	dependenciesForTest(model).TmuxSessionExists = func(_ context.Context, name string) (bool, error) {
		return true, nil
	}
	dependenciesForTest(model).TmuxIsInsideTmux = func() bool {
		return true
	}
	dependenciesForTest(model).TmuxSwitchSession = func(context.Context, string) error {
		return nil
	}

	cmdFn := model.handleActionKeys(keyText("x"))
	if cmdFn == nil {
		t.Fatal("handleActionKeys(select_0) returned nil")
	}

	if got, want := model.cursor, 9; got != want {
		t.Fatalf("cursor after select_0 = %d, want %d", got, want)
	}
}

func TestVisibleShortcutSelectsSessionWithoutAgents(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{{
		Name: "dev",
	}}

	handler := model.handleActionKeys(keyCtrl('1'))
	if handler == nil {
		t.Fatal("handleActionKeys(ctrl+1) returned nil")
	}

	if model.cursor != 0 || model.agentCursor != parentAgentIndex {
		t.Fatalf("shortcut selection = %d/%d, want session heading", model.cursor, model.agentCursor)
	}

	_, cmd := handler()

	model.Update(cmd())

	if model.DeferredSwitchSession() != "dev" || model.DeferredSwitchPane() != "" {
		t.Fatalf("shortcut session target = %q/%q, want dev without pane", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestVisibleShortcutSelectsExactAgentChild(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{{
		Name: "dev",
		Agents: []session.AgentSession{{
			Harness: "pi", HarnessSession: "agent-id", PaneID: "%7",
		}},
	}}

	handler := model.handleActionKeys(keyCtrl('1'))
	if handler == nil {
		t.Fatal("handleActionKeys(ctrl+1) returned nil")
	}

	if model.cursor != 0 || model.agentCursor != 0 {
		t.Fatalf("shortcut selection = %d/%d, want first child", model.cursor, model.agentCursor)
	}

	_, cmd := handler()

	model.Update(cmd())

	if model.DeferredSwitchSession() != "dev" || model.DeferredSwitchPane() != "%7" {
		t.Fatalf("shortcut child target = %q/%q, want dev pane %%7", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func twoSessionAgentModel() *Model {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{
		{
			Name: "session-a",
			Agents: []session.AgentSession{
				{Harness: "pi", PaneID: "%1"},
				{Harness: "pi", PaneID: "%2"},
			},
		},
		{
			Name: "session-b",
			Agents: []session.AgentSession{
				{Harness: "pi", PaneID: "%3"},
			},
		},
	}

	return model
}

func TestVisibleShortcutScopesToSelectedSessionInAgentsMode(t *testing.T) {
	model := twoSessionAgentModel()
	model.cursor = 0

	handler := model.handleActionKeys(keyCtrl('2'))
	if handler == nil {
		t.Fatal("handleActionKeys(ctrl+2) for session-a returned nil")
	}

	if model.cursor != 0 || model.agentCursor != 1 {
		t.Fatalf("session-a selection = %d/%d, want 0/1", model.cursor, model.agentCursor)
	}

	_, cmd := handler()

	model.Update(cmd())

	if model.DeferredSwitchSession() != "session-a" || model.DeferredSwitchPane() != "%2" {
		t.Fatalf("session-a target = %q/%q, want session-a pane %%2", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}
}

func TestVisibleShortcutSwitchesWithSessionCursorInAgentsMode(t *testing.T) {
	model := twoSessionAgentModel()
	model.cursor = 1

	handlerB := model.handleActionKeys(keyCtrl('1'))
	if handlerB == nil {
		t.Fatal("handleActionKeys(ctrl+1) for session-b returned nil")
	}

	if model.cursor != 1 || model.agentCursor != 0 {
		t.Fatalf("session-b selection = %d/%d, want 1/0", model.cursor, model.agentCursor)
	}

	_, cmdB := handlerB()

	model.Update(cmdB())

	if model.DeferredSwitchSession() != "session-b" || model.DeferredSwitchPane() != "%3" {
		t.Fatalf("session-b target = %q/%q, want session-b pane %%3", model.DeferredSwitchSession(), model.DeferredSwitchPane())
	}

	handlerMissing := model.handleActionKeys(keyCtrl('2'))
	if handlerMissing == nil {
		t.Fatal("handleActionKeys(ctrl+2) for missing slot returned nil handler")
	}

	_, cmdMissing := handlerMissing()
	if cmdMissing != nil {
		t.Fatalf("missing slot command = %v, want nil", cmdMissing)
	}
}

func TestVisibleSessionShortcutNoopsWhenSlotMissing(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 8
	model.filtered = testSessions(3)
	model.config.Keys.Select9 = "x"

	cmdFn := model.handleActionKeys(keyText("x"))
	if cmdFn == nil {
		t.Fatal("handleActionKeys(select_9) returned nil")
	}

	_, cmd := cmdFn()
	if cmd != nil {
		t.Fatalf("missing visible slot command = %T, want nil", cmd)
	}

	if got, want := model.cursor, 0; got != want {
		t.Fatalf("cursor after missing visible slot = %d, want %d", got, want)
	}
}

func TestDefaultAltManagementKeysTriggerActions(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: "dev", Kind: session.KindLive}}
	model.filtered = []session.Session{{Name: "dev", Kind: session.KindLive}}

	if cmd := model.handleActionKeys(keyAlt("r")); cmd == nil {
		t.Fatal("handleActionKeys(alt+r) returned nil")
	} else {
		cmd()
	}

	if model.dialog != dialogInputText || model.pending.entry.Describe().ID != action.SessionRename.ID {
		t.Fatalf("dialog after alt+r = %v, want rename dialog", model.dialog)
	}

	model.cancelAction()

	if cmd := model.handleActionKeys(keyAlt("x")); cmd == nil {
		t.Fatal("handleActionKeys(alt+x) returned nil")
	} else {
		cmd()
	}

	if model.dialog != dialogConfirm || model.pending.entry.Describe().ID != action.SessionKill.ID {
		t.Fatalf("dialog after alt+x = %v, want confirm kill dialog", model.dialog)
	}

	model.cancelAction()

	if cmd := model.handleActionKeys(keyAlt("X")); cmd == nil {
		t.Fatal("handleActionKeys(alt+shift+x) returned nil")
	} else {
		cmd()
	}

	if model.dialog != dialogConfirm || model.pending.entry.Describe().ID != action.SessionDelete.ID {
		t.Fatalf("dialog after alt+shift+x = %v, want confirm delete dialog", model.dialog)
	}
}

func TestTypingNumberInPopupSearches(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 12
	model.filtered = testSessions(5)

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: '2', Text: "2"}))

	m, ok := updated.(*Model)
	if !ok || m.textInput.Value() != "2" {
		t.Fatalf("search input = %q after pressing '2', want '2'", m.textInput.Value())
	}
}
