package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

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
	if !strings.Contains(output, ansiFooterHoverKey+model.config.Keys.Create) {
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
