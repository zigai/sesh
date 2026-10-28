package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestMenuKeyOpensBesideTheSelectedRow(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        uiMode
		display     displayMode
		height      int
		sessions    int
		cursor      int
		agentCursor int
		selectedY   int
		menuY       int
		menuBottomY int
	}{
		{name: "first session", mode: modePopup, display: displayModeSesh, height: 24, sessions: 4, agentCursor: -1, selectedY: 2, menuY: 3, menuBottomY: 7},
		{name: "later session", mode: modePopup, display: displayModeSesh, height: 24, sessions: 4, cursor: 2, agentCursor: -1, selectedY: 4, menuY: 5, menuBottomY: 9},
		{name: "scrolled list", mode: modePopup, display: displayModeSesh, height: 12, sessions: 20, cursor: 19, agentCursor: -1, selectedY: 9, menuY: 4, menuBottomY: 8},
		{name: "agent row", mode: modePopup, display: displayModeAgents, height: 24, sessions: 1, agentCursor: 1, selectedY: 4, menuY: 5, menuBottomY: 7},
		{name: "bottom row", mode: modePopup, display: displayModeSesh, height: 12, sessions: 8, cursor: 7, agentCursor: -1, selectedY: 9, menuY: 4, menuBottomY: 8},
		{name: "short window", mode: modePopup, display: displayModeSesh, height: 6, sessions: 2, cursor: 1, agentCursor: -1, selectedY: 3, menuY: 0, menuBottomY: 4},
		{name: "fullscreen", mode: modeFullscreen, display: displayModeSesh, height: 24, sessions: 4, cursor: 2, agentCursor: -1, selectedY: 4, menuY: 5, menuBottomY: 9},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel()
			model.mode = test.mode
			model.displayMode = test.display
			model.config.Menu.Session = []string{config.MenuGo, config.MenuRename, config.MenuKill}
			model.config.Menu.Agent = []string{config.MenuGo}
			model.filtered = testSessions(test.sessions)
			model.cursor = test.cursor

			model.agentCursor = test.agentCursor
			if test.display == displayModeAgents {
				model.filtered[0].Agents = []session.AgentSession{
					{RegistryID: "worker-a", Harness: "agent"},
					{RegistryID: "worker-b", Harness: "agent"},
				}
				model.config.Menu.Session = nil
			}

			model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: test.height})

			before := strings.Split(stripANSI(viewString(model.View())), "\n")
			if !strings.HasPrefix(before[test.selectedY], "│ ▌ ") {
				t.Fatalf("selected row at line %d missing before opening menu:\n%s", test.selectedY, strings.Join(before, "\n"))
			}

			model = updateTestModel(t, model, keyAlt("m"))
			output := stripANSI(viewString(model.View()))
			lines := strings.Split(output, "\n")

			for _, corner := range []struct {
				glyph string
				y     int
			}{
				{glyph: "╭", y: test.menuY},
				{glyph: "╰", y: test.menuBottomY},
			} {
				prefix, _, ok := strings.Cut(lines[corner.y], corner.glyph)
				if !ok || lipgloss.Width(prefix) != 4 {
					t.Fatalf("menu corner %s is not at column 4, line %d:\n%s", corner.glyph, corner.y, output)
				}
			}
		})
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
