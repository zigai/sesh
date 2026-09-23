package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestSearchQueryChangeResetsCursorToFirstResult(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	model.filtered = append([]session.Session(nil), model.sessions...)
	model.cursor = 1

	model = updateTestModel(t, model, keyText("a"))

	if got := model.cursor; got != 0 {
		t.Fatalf("cursor after search query change = %d, want first result", got)
	}
}

func TestSearchUpdatesResultsWithoutClearingScreen(t *testing.T) {
	for _, mode := range []uiMode{modePopup, modeStandalone, modeInline} {
		synctest.Test(t, func(t *testing.T) {
			model := newTestModel()
			t.Cleanup(model.Close)
			model.mode = mode
			model.width, model.height = 80, 24
			model.displayMode = displayModeAgents
			model.sessions = []session.Session{
				{Name: "codex", Agents: []session.AgentSession{{Harness: "pi"}}},
				{Name: "other", Agents: []session.AgentSession{{Harness: "pi"}}},
			}
			model.refreshDisplaySessions()
			model.filterSessions()
			initialLines := strings.Split(viewString(model.View()), "\n")

			for _, step := range []struct {
				key  tea.KeyPressMsg
				want []string
			}{
				{key: keyText("c"), want: []string{"codex"}},
				{key: keyText("z"), want: nil},
				{key: keyCtrl('u'), want: []string{"codex", "other"}},
			} {
				_, cmd := model.Update(step.key)
				assertNoScreenClear(t, cmd)
				assertSearchResultsFrame(t, model, step.want, initialLines)
			}
		})
	}
}

func assertSearchResultsFrame(t *testing.T, model *Model, want []string, initialLines []string) {
	t.Helper()

	if got := sessionNamesForTest(model.filtered); !slices.Equal(got, want) {
		t.Fatalf("filtered sessions = %v, want %v", got, want)
	}

	rendered := viewString(model.View())

	lines := strings.Split(rendered, "\n")
	if len(lines) != len(initialLines) || lines[0] != initialLines[0] || lines[len(lines)-1] != initialLines[len(initialLines)-1] {
		t.Fatal("filtering changed the surrounding frame")
	}

	for _, removed := range []string{"codex", "other"} {
		if !slices.Contains(want, removed) && strings.Contains(stripANSI(rendered), removed) {
			t.Fatalf("filtered-out row %q remains in the rendered frame", removed)
		}
	}
}

func assertNoScreenClear(t *testing.T, cmd tea.Cmd) {
	t.Helper()

	if cmd == nil {
		return
	}

	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			assertNoScreenClear(t, child)
		}
	} else if reflect.TypeOf(msg) == reflect.TypeOf(tea.ClearScreen()) {
		t.Fatal("ordinary search update requested a full-screen clear")
	}
}

func TestPrintableManagementLettersFilterSearchByDefault(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: "prod"}, {Name: "rust"}, {Name: "xray"}}
	model.filtered = append([]session.Session(nil), model.sessions...)

	for _, key := range []rune{'p', 'u', 'r', 'x'} {
		model.dialog = dialogNone
		model.textInput.SetValue("")
		model.filterSessions()

		model = updateTestModel(t, model, keyText(string(key)))

		if got := model.textInput.Value(); got != string(key) {
			t.Fatalf("text input after %q = %q, want search query", string(key), got)
		}

		if model.dialog != dialogNone {
			t.Fatalf("dialog after %q = %v, want none", string(key), model.dialog)
		}
	}
}

func TestSearchInputUsesCommonShellEditingKeys(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: "alpha"}, {Name: "beta"}}
	model.filtered = append([]session.Session(nil), model.sessions...)

	model.textInput.SetValue("alpha beta")

	model = updateTestModel(t, model, keyCtrl('a'))
	model = updateTestModel(t, model, keyText("X"))

	if got := model.textInput.Value(); got != "Xalpha beta" {
		t.Fatalf("after ctrl+a insert = %q, want Xalpha beta", got)
	}

	model = updateTestModel(t, model, keyCtrl('e'))
	model = updateTestModel(t, model, keyText("!"))

	if got := model.textInput.Value(); got != "Xalpha beta!" {
		t.Fatalf("after ctrl+e insert = %q, want Xalpha beta!", got)
	}

	model = updateTestModel(t, model, keyCtrl('w'))

	if got := model.textInput.Value(); got != "Xalpha " {
		t.Fatalf("after ctrl+w = %q, want deleted previous word", got)
	}

	model = updateTestModel(t, model, keyCtrl('u'))

	if got := model.textInput.Value(); got != "" {
		t.Fatalf("after ctrl+u = %q, want empty input", got)
	}

	model.textInput.SetValue("abcdef")
	model = updateTestModel(t, model, keyCtrl('a'))
	model = updateTestModel(t, model, keyCode(tea.KeyRight))

	model = updateTestModel(t, model, keyCode(tea.KeyDelete))
	if got := model.textInput.Value(); got != "acdef" {
		t.Fatalf("after right/delete = %q, want acdef", got)
	}

	model = updateTestModel(t, model, keyCtrl('a'))

	model = updateTestModel(t, model, keyCtrl('k'))
	if got := model.textInput.Value(); got != "" {
		t.Fatalf("after ctrl+k = %q, want empty input", got)
	}
}
