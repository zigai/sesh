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

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestRightClickOpensMenuAtTheClickPosition(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.config.Menu.Session = []string{config.MenuGo, config.MenuRename, config.MenuKill}
	model.filtered = testSessions(3)
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})
	model.View()

	model = updateTestModel(t, model, mouseClick(30, 3, tea.MouseRight))
	output := stripANSI(viewString(model.View()))
	lines := strings.Split(output, "\n")

	if !strings.HasPrefix(lines[3], "│ ▌ session-1") {
		t.Fatalf("right-click did not select the clicked session:\n%s", output)
	}

	for _, corner := range []struct {
		glyph string
		y     int
	}{
		{glyph: "╭", y: 4},
		{glyph: "╰", y: 8},
	} {
		prefix, _, ok := strings.Cut(lines[corner.y], corner.glyph)
		if !ok || lipgloss.Width(prefix) != 30 {
			t.Fatalf("menu corner %s is not at column 30, line %d:\n%s", corner.glyph, corner.y, output)
		}
	}
}

func TestMouseWheelScrollsPopupList(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 50
	model.height = 10

	model.filtered = make([]session.Session, 10)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: fmt.Sprintf("session-%d", i)}
	}

	layout := model.popupMouseLayout()

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelDown))
	if got := model.visibleSessionStart(layout.layout.listHeight); got != 3 {
		t.Fatalf("visible start after wheel down = %d, want 3", got)
	}

	if got := model.cursor; got != 3 {
		t.Fatalf("cursor after wheel down = %d, want first visible row 3", got)
	}

	output := stripANSI(viewString(model.View()))
	if strings.Contains(output, "session-0") || !strings.Contains(output, "session-3") {
		t.Fatalf("popup after wheel down = %q, want list scrolled to session-3", output)
	}

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))
	if got := model.visibleSessionStart(layout.layout.listHeight); got != 0 {
		t.Fatalf("visible start after wheel up = %d, want 0", got)
	}
}

func TestMouseWheelUpRevealsEarlierAgentGroup(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		hoverChild bool
	}{
		{name: "selected heading"},
		{name: "selected first child", hoverChild: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model := newTestModel()
			model.mode = modePopup
			model.displayMode = displayModeAgents
			model.agentCursor = parentAgentIndex
			model.width = 80
			model.height = 9
			model.filtered = []session.Session{
				{Name: "earlier-project", Agents: threeAgentsForTest()},
				{Name: "middle-project", Agents: threeAgentsForTest()},
				{Name: "later-project", Agents: threeAgentsForTest()},
			}

			layout := model.popupMouseLayout()
			model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelDown))

			model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+1))
			if testCase.hoverChild {
				model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+2))
				if model.agentCursor != 0 {
					t.Fatalf("hovered agent = %d, want first child", model.agentCursor)
				}
			}

			output := stripANSI(viewString(model.View()))
			if strings.Contains(output, "earlier-project") || !strings.Contains(output, "middle-project") {
				t.Fatalf("popup after wheel down = %q, want middle-project heading without earlier-project", output)
			}

			model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))

			output = stripANSI(viewString(model.View()))
			if !strings.Contains(output, "earlier-project") {
				t.Fatalf("popup after wheel up = %q, want earlier-project heading", output)
			}
		})
	}
}

func TestMouseWheelUpWithinTallAgentGroupPreservesVisibleSelection(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.agentCursor = parentAgentIndex
	model.width = 80
	model.height = 9

	model.filtered = []session.Session{
		{Name: "tall-project", Agents: make([]session.AgentSession, 7)},
		{Name: "later-project", Agents: threeAgentsForTest()},
	}
	for index := range model.filtered[0].Agents {
		model.filtered[0].Agents[index] = session.AgentSession{Harness: "codex", HarnessSession: fmt.Sprintf("agent-%d", index)}
	}

	layout := model.popupMouseLayout()
	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelDown))
	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelDown))

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))
	if got := model.agentCursor; got != 5 {
		t.Fatalf("selected agent after wheel up = %d, want visible agent 5", got)
	}

	output := stripANSI(viewString(model.View()))
	if !strings.Contains(output, "agent-2") || !strings.Contains(output, "agent-5") {
		t.Fatalf("popup after wheel up = %q, want earlier agent-2 and selected agent-5", output)
	}

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))

	output = stripANSI(viewString(model.View()))
	if !strings.Contains(output, "tall-project") {
		t.Fatalf("popup after second wheel up = %q, want tall-project heading", output)
	}
}

func TestMouseHoverSelectsPopupRow(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 50
	model.height = 10
	model.filtered = []session.Session{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}

	layout := model.popupMouseLayout()

	model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+2))
	if got := model.cursor; got != 2 {
		t.Fatalf("cursor after hovering third row = %d, want 2", got)
	}
}

func TestMouseClickPopupRowSelectsAndSubmits(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		TmuxIsInsideTmux: func() bool { return true },
	})
	model.mode = modePopup
	model.width = 50
	model.height = 10
	model.filtered = []session.Session{
		{Name: "alpha", Kind: session.KindLive},
		{Name: "beta", Kind: session.KindLive},
	}

	layout := model.popupMouseLayout()

	updated, cmd := model.Update(mouseClick(layout.bodyX, layout.listY+1, tea.MouseLeft))

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}

	if got := next.cursor; got != 1 {
		t.Fatalf("cursor after clicking second row = %d, want 1", got)
	}

	if cmd == nil {
		t.Fatal("row click returned nil command, want submitted session action")
	}

	msg, ok := cmd().(sessionActionResultMsg)
	if !ok {
		t.Fatalf("row click command returned %T, want sessionActionResultMsg", msg)
	}

	if msg.sessionName != "beta" || msg.switchSession != "beta" || !msg.quit {
		t.Fatalf("row click msg = %#v, want deferred popup switch to beta", msg)
	}
}

func TestMouseClickFooterShortcutTriggersAction(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 140
	model.height = 10
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	x, y := footerPartPoint(t, model, "help")
	model = updateTestModel(t, model, mouseClick(x, y, tea.MouseLeft))

	if !model.showHelp {
		t.Fatal("showHelp = false after clicking help footer shortcut, want true")
	}
}

func TestMouseHoverFooterShortcutHighlightsButton(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 140
	model.height = 10
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	x, y := footerPartPoint(t, model, "create")
	model = updateTestModel(t, model, mouseMotion(x, y))

	if model.footerHoverDesc != "create" {
		t.Fatalf("footerHoverDesc = %q, want create", model.footerHoverDesc)
	}

	output := viewString(model.View())
	if !strings.Contains(output, ansiFooterHoverKey+"alt+c") {
		t.Fatalf("footer output = %q, expected hovered create key", output)
	}

	if !strings.Contains(output, ansiFooterHoverDesc+" create") {
		t.Fatalf("footer output = %q, expected hovered create description", output)
	}

	model = updateTestModel(t, model, mouseMotion(x, y-1))
	if model.footerHoverDesc != "" {
		t.Fatalf("footerHoverDesc after leaving footer = %q, want empty", model.footerHoverDesc)
	}
}

func TestMouseClickSearchInputMovesCursor(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 50
	model.height = 10
	model.textInput.SetValue("abcdef")
	model.textInput.CursorEnd()
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	layout := model.popupMouseLayout()
	model = updateTestModel(t, model, mouseClick(layout.inputX+2, layout.inputY, tea.MouseLeft))

	if got := model.textInput.Position(); got != 2 {
		t.Fatalf("search cursor after input click = %d, want 2", got)
	}
}

func footerPartPoint(t *testing.T, model *Model, desc string) (int, int) {
	t.Helper()

	layout := model.popupMouseLayout()

	x := layout.bodyX
	for _, part := range model.footerParts(layout.layout.bodyWidth) {
		if part.desc == desc {
			return x, layout.footerY
		}

		x += part.width + 2
	}

	t.Fatalf("footer shortcut %q not visible in %#v", desc, model.footerParts(layout.layout.bodyWidth))

	return 0, 0
}

func TestMouseClickConfirmDialogButtons(t *testing.T) {
	for _, testCase := range []struct {
		button     string
		wantKilled string
	}{
		{button: "Yes", wantKilled: "session-1"},
		{button: "No", wantKilled: ""},
	} {
		t.Run(testCase.button, func(t *testing.T) {
			model := newTestModel()
			model.mode = modePopup
			model.filtered = testSessions(3)
			model.cursor = 1
			model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})

			killed := ""
			dependenciesForTest(model).TmuxKillSession = func(_ context.Context, name string) error {
				killed = name

				return nil
			}

			openTestKillConfirm(model)

			x, y := renderedTextPoint(t, model, " "+testCase.button+" ")
			model = updateTestModelWithCommand(t, model, mouseClick(x+1, y, tea.MouseLeft))

			if killed != testCase.wantKilled || model.dialog != dialogNone {
				t.Fatalf("after clicking %s: killed=%q dialog=%v, want killed=%q and dialog closed", testCase.button, killed, model.dialog, testCase.wantKilled)
			}
		})
	}
}

func TestMouseHoverConfirmDialogButtonSelectsIt(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.filtered = testSessions(2)
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})
	openTestKillConfirm(model)

	x, y := renderedTextPoint(t, model, " Yes ")
	model = updateTestModel(t, model, mouseMotion(x+1, y))

	if !model.confirmYes {
		t.Fatal("confirmYes = false after hovering Yes, want true")
	}

	model = updateTestModel(t, model, mouseClick(0, 0, tea.MouseLeft))

	if model.dialog != dialogConfirm {
		t.Fatalf("dialog after clicking outside = %v, want confirmation still open", model.dialog)
	}
}

func TestMouseClickPathSuggestionCompletesIt(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})

	cwd := t.TempDir()
	for _, dir := range []string{"docs", filepath.Join("beta", "child")} {
		if err := os.MkdirAll(filepath.Join(cwd, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}

	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	openTestCreateDirectoryPrompt(model, "new-session")
	model.refreshPathSuggestions()

	beta := "beta" + string(os.PathSeparator)
	x, y := renderedTextPoint(t, model, beta)
	model = updateTestModel(t, model, mouseClick(x, y, tea.MouseLeft))

	if got := model.pathInput.Value(); got != beta {
		t.Fatalf("path input after clicking suggestion = %q, want %q", got, beta)
	}

	wantChild := filepath.Join("beta", "child") + string(os.PathSeparator)
	if len(model.pathSuggestions) != 1 || model.pathSuggestions[0] != wantChild {
		t.Fatalf("suggestions after clicking beta = %#v, want [%q]", model.pathSuggestions, wantChild)
	}
}

func TestMouseClickDismissesHelpOverlay(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})
	model.showHelp = true

	model = updateTestModel(t, model, mouseClick(5, 5, tea.MouseLeft))

	if model.showHelp {
		t.Fatal("showHelp = true after clicking, want help dismissed")
	}
}

func renderedTextPoint(t *testing.T, model *Model, text string) (int, int) {
	t.Helper()

	output := stripANSI(viewString(model.View()))
	for y, line := range strings.Split(output, "\n") {
		if prefix, _, ok := strings.Cut(line, text); ok {
			return lipgloss.Width(prefix), y
		}
	}

	t.Fatalf("%q not rendered:\n%s", text, output)

	return 0, 0
}
