package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestRenderCreateDirDialogShowsInputAndSuggestions(t *testing.T) {
	model := newTestModel()
	model.width = 90
	model.height = 24
	openTestCreateDirectoryPrompt(model, testSupportNewSessionName)
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/api/", "/work/web/"}

	output := stripANSI(model.renderDialog())
	for _, want := range []string{"Create session", "Session name: new-session", "Initial working directory:", "/work/", "/work/api/", "/work/web/"} {
		if !strings.Contains(output, want) {
			t.Fatalf("create dir dialog = %q, expected %q", output, want)
		}
	}
}

func TestHelpOverlayRendersConfiguredKeysAndDismisses(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 28
	model.config.Keys.Help = "f1"

	model.filtered = make([]session.Session, 20)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: fmt.Sprintf("session-%02d", i)}
	}

	if cmd := model.handleActionKeys(keyCode(tea.KeyF1)); cmd == nil {
		t.Fatal("handleActionKeys(help) returned nil")
	} else {
		cmd()
	}

	if !model.showHelp {
		t.Fatal("showHelp = false, want true")
	}

	output := stripANSI(viewString(model.View()))
	for _, needle := range []string{"Popup Keys", "f1", "Page up/down", "Jump to top/bottom"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("help overlay = %q, expected %q", output, needle)
		}
	}

	if !strings.Contains(output, "session-00") {
		t.Fatalf("help overlay = %q, expected underlying session list to remain visible", output)
	}

	_, _ = model.Update(keyText("x"))
	if model.showHelp {
		t.Fatal("showHelp = true after dismiss key, want false")
	}
}

func TestHelpOverlayShowsHelpShortcutLast(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 24
	model.showHelp = true

	output := stripANSI(model.renderHelpOverlay())

	closeIndex := strings.Index(output, "Close popup")
	helpIndex := strings.Index(output, "Show this help")

	dismissIndex := strings.Index(output, "Press any key to dismiss")
	if closeIndex == -1 || helpIndex == -1 || dismissIndex == -1 {
		t.Fatalf("help overlay = %q, expected close, help, and dismiss text", output)
	}

	if closeIndex >= helpIndex || helpIndex >= dismissIndex {
		t.Fatalf("help overlay order = close:%d help:%d dismiss:%d, want help shortcut last", closeIndex, helpIndex, dismissIndex)
	}
}

func TestDialogRendersOverPopupContent(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 20
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}
	invokeTestAction(t, model, action.SessionKill.ID)

	output := stripANSI(viewString(model.View()))
	for _, needle := range []string{testSupportSessionDevName, "Confirm", "Kill session dev?", "Yes", "No"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("dialog overlay = %q, expected %q", output, needle)
		}
	}

	if strings.Contains(output, "(y/n)") {
		t.Fatalf("dialog overlay = %q, should not render y/n prompt text", output)
	}

	assertRightBorder(t, viewString(model.View()), 80)
}

func TestDialogStaysInsideConfiguredNarrowPopupWidth(t *testing.T) {
	model := newTestModel()
	model.width = 48
	model.height = 20
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	invokeTestAction(t, model, action.SessionRename.ID)
	model.textParamInput.SetValue("test-rename")

	dialog := model.renderDialog()
	if width := lipgloss.Width(dialog); width > model.width {
		t.Fatalf("rename dialog width = %d, want <= popup width %d", width, model.width)
	}

	model.cancelAction()
	invokeTestAction(t, model, action.SessionKill.ID)

	dialog = model.renderDialog()
	if width := lipgloss.Width(dialog); width > model.width {
		t.Fatalf("confirm dialog width = %d, want <= popup width %d", width, model.width)
	}
}

func TestDeleteDialogUsesShortPromptAndButtons(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 20
	model.filtered = []session.Session{{Name: "archive", Kind: session.KindSaved}}

	invokeTestAction(t, model, action.SessionDelete.ID)

	output := stripANSI(viewString(model.View()))
	for _, needle := range []string{"Confirm", "Delete session archive?", "Yes", "No"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("delete dialog = %q, expected %q", output, needle)
		}
	}

	if strings.Contains(output, "(y/n)") {
		t.Fatalf("delete dialog = %q, should not contain a y/n prompt", output)
	}
}

func TestConfirmButtonsRenderCompactSingleLine(t *testing.T) {
	model := newTestModel()
	rendered := renderConfirmButtons(true, model.config)

	output := stripANSI(rendered)
	if strings.Contains(output, "\n") {
		t.Fatalf("confirm buttons = %q, should stay on one line", output)
	}

	if got := lipgloss.Width(rendered); got > 20 {
		t.Fatalf("confirm buttons width = %d, want compact row", got)
	}
}

func TestConfirmDialogEnterUsesSelectedButtonAndShortcutsStillWork(t *testing.T) {
	confirmed := 0

	model := newTestModelWithDeps(Dependencies{
		RunAction: func(context.Context, action.Invocation) (any, error) {
			confirmed++

			return action.KilledSession{Session: testLiveRef("$1", testSupportSessionDevName)}, nil
		},
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
	})
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	invokeTestAction(t, model, action.SessionKill.ID)
	pressTestKey(t, model, keyCode(tea.KeyEnter))

	if confirmed != 0 {
		t.Fatalf("enter on default No confirmed %d times, want 0", confirmed)
	}

	invokeTestAction(t, model, action.SessionKill.ID)
	pressTestKey(t, model, keyCode(tea.KeyLeft))
	pressTestKey(t, model, keyCode(tea.KeyEnter))

	if confirmed != 1 {
		t.Fatalf("enter on selected Yes confirmed %d times, want 1", confirmed)
	}

	invokeTestAction(t, model, action.SessionKill.ID)
	pressTestKey(t, model, keyText("y"))

	if confirmed != 2 {
		t.Fatalf("y shortcut confirmed %d times, want 2", confirmed)
	}

	invokeTestAction(t, model, action.SessionKill.ID)
	model.confirmYes = true
	pressTestKey(t, model, keyText("n"))

	if model.dialog != dialogNone {
		t.Fatalf("n shortcut left dialog = %v, want none", model.dialog)
	}

	if confirmed != 2 {
		t.Fatalf("n shortcut confirmed %d times, want 2", confirmed)
	}
}

func TestHelpOverlayListsTheSameKeysForEveryRowOfAView(t *testing.T) {
	model := newTestModel()
	model.width = 96
	model.height = 30
	model.filtered = []session.Session{{Name: "current", Kind: session.KindLive}, {Name: "archive", Kind: session.KindSaved}}
	model.showHelp = true

	live := stripANSI(model.renderHelpOverlay())
	model.cursor = 1

	if saved := stripANSI(model.renderHelpOverlay()); saved != live {
		t.Fatalf("saved row help = %q, want the live row help %q", saved, live)
	}

	for _, want := range []string{"Rename session", "Kill session", "Delete session"} {
		if !strings.Contains(live, want) {
			t.Fatalf("help overlay = %q, expected %q", live, want)
		}
	}

	for _, unwanted := range []string{"Sleep task", "Finish"} {
		if strings.Contains(live, unwanted) {
			t.Fatalf("sessions help = %q, should omit inbox action %q", live, unwanted)
		}
	}
}

func TestHelpOverlayExplainsAgentAndTmuxGroupNavigation(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 96
	model.height = 30
	model.filtered = []session.Session{{
		Name:   "project",
		Kind:   session.KindLive,
		Agents: []session.AgentSession{{RegistryID: "agent-1"}},
	}}
	model.agentCursor = 0
	model.showHelp = true

	output := stripANSI(model.renderHelpOverlay())
	for _, want := range []string{
		"Move between agents",
		"Move between tmux sessions",
		"Rename session",
		"Kill session",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("agent help = %q, want %q", output, want)
		}
	}

	for _, unwanted := range []string{"collapse agents", "Move within selected session"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("agent help = %q, should omit %q", output, unwanted)
		}
	}
}

func TestRenderCreateDirDialogMarksPathsThatAreNotDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "existing"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		path   string
		marker string
	}{
		"missing directory":  {path: filepath.Join(root, "missing"), marker: "will be created"},
		"existing directory": {path: filepath.Join(root, "existing"), marker: ""},
		"regular file":       {path: filepath.Join(root, "file"), marker: "not a directory"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			model := newTestModel()
			model.width = 90
			model.height = 24
			dependenciesForTest(model).GetWorkingDirectory = func() (string, error) { return root, nil }
			openTestCreateDirectoryPrompt(model, testSupportNewSessionName)
			model.setPathInputValue(tc.path)
			model.refreshPathSuggestions()

			output := stripANSI(model.renderDialog())
			for _, marker := range []string{"will be created", "not a directory"} {
				if got := strings.Contains(output, marker); got != (marker == tc.marker) {
					t.Fatalf("dialog shows %q = %v, want marker %q:\n%s", marker, got, tc.marker, output)
				}
			}
		})
	}
}
