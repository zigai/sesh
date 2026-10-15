package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

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
