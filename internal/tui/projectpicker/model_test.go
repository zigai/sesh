package projectpicker_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/tui/projectpicker"
)

func TestPickerListsOpenProjectsFirstAndOpensTheSelection(t *testing.T) {
	model := newModel(t, []projectpicker.Entry{
		{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: ""},
		{Root: "/code/beta", Name: "beta", Path: "~/code/beta", Session: "beta", Taken: ""},
	})

	if first := listLines(t, model)[0]; !strings.Contains(first, "beta") {
		t.Fatalf("first row = %q, want the project with an open session", first)
	}

	send(model, key(tea.KeyDown), key(tea.KeyEnter))

	if choice, ok := model.Chosen(); !ok || choice != (projectpicker.Choice{Kind: projectpicker.ChoiceOpen, Value: "/code/alpha"}) {
		t.Fatalf("Chosen() = %+v, %t; want alpha opened", choice, ok)
	}
}

func TestPickerOffersToCloneURLsAndCreateUnknownNames(t *testing.T) {
	tests := []struct {
		query string
		want  projectpicker.Choice
	}{
		{"https://example.com/team/widget.git", projectpicker.Choice{Kind: projectpicker.ChoiceClone, Value: "https://example.com/team/widget.git"}},
		{"git@example.com:team/widget.git", projectpicker.Choice{Kind: projectpicker.ChoiceClone, Value: "git@example.com:team/widget.git"}},
		{"notebook", projectpicker.Choice{Kind: projectpicker.ChoiceCreate, Value: "notebook"}},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			model := newModel(t, []projectpicker.Entry{{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: ""}})
			send(model, typed(tt.query)...)
			send(model, key(tea.KeyEnter))

			if choice, ok := model.Chosen(); !ok || choice != tt.want {
				t.Fatalf("Chosen() = %+v, %t; want %+v", choice, ok, tt.want)
			}
		})
	}
}

func TestPasteUpdatesTheProjectChoices(t *testing.T) {
	for _, test := range []struct {
		query string
		want  projectpicker.Choice
	}{
		{"beta", projectpicker.Choice{Kind: projectpicker.ChoiceOpen, Value: "/code/beta"}},
		{"https://example.com/team/widget.git", projectpicker.Choice{Kind: projectpicker.ChoiceClone, Value: "https://example.com/team/widget.git"}},
		{"notebook", projectpicker.Choice{Kind: projectpicker.ChoiceCreate, Value: "notebook"}},
	} {
		t.Run(test.query, func(t *testing.T) {
			model := newModel(t, []projectpicker.Entry{
				{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: ""},
				{Root: "/code/beta", Name: "beta", Path: "~/code/beta", Session: "", Taken: ""},
			})

			send(model, tea.PasteMsg{Content: test.query}, key(tea.KeyEnter))

			if choice, ok := model.Chosen(); !ok || choice != test.want {
				t.Fatalf("Chosen() = %+v, %t; want %+v", choice, ok, test.want)
			}
		})
	}
}

func TestPickerDoesNotOfferToCreateAProjectThatExists(t *testing.T) {
	model := newModel(t, []projectpicker.Entry{{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: ""}})
	send(model, typed("alpha")...)

	for _, line := range listLines(t, model) {
		if strings.Contains(line, "create") {
			t.Fatalf("picker offers %q for an existing project", line)
		}
	}
}

func TestPickerAdoptsATakenNameOnlyWhenAsked(t *testing.T) {
	model := newModel(t, []projectpicker.Entry{{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: "alpha"}})

	send(model, key(tea.KeyEnter))

	if _, ok := model.Chosen(); ok {
		t.Fatal("enter chose a project whose name another session uses")
	}

	if view := plainView(model); !strings.Contains(view, "alt+a adopts it") {
		t.Fatalf("view does not explain how to adopt: %q", view)
	}

	send(model, tea.KeyPressMsg(tea.Key{Code: 'a', Mod: tea.ModAlt}))

	if choice, ok := model.Chosen(); !ok || choice != (projectpicker.Choice{Kind: projectpicker.ChoiceAdopt, Value: "/code/alpha"}) {
		t.Fatalf("Chosen() = %+v, %t; want alpha adopted", choice, ok)
	}
}

func TestPickerCancelChoosesNothing(t *testing.T) {
	model := newModel(t, []projectpicker.Entry{{Root: "/code/alpha", Name: "alpha", Path: "~/code/alpha", Session: "", Taken: ""}})
	send(model, key(tea.KeyEscape))

	if _, ok := model.Chosen(); ok {
		t.Fatal("escape chose a project")
	}
}

func TestPickerFrameLinesKeepTheirWidth(t *testing.T) {
	long := strings.Repeat("deep/", 30) + "project"
	model := newModel(t, []projectpicker.Entry{
		{Root: "/code/alpha", Name: "alpha", Path: "~/" + long, Session: "alpha", Taken: ""},
		{Root: "/code/beta", Name: "beta", Path: "~/code/beta", Session: "", Taken: "beta"},
	})

	for _, query := range []string{"", "a", "notebook"} {
		send(model, typed(query)...)

		lines := strings.SplitSeq(plainView(model), "\n")
		for line := range lines {
			if width := ansi.StringWidth(line); width != 70 {
				t.Fatalf("query %q: line %q is %d columns, want 70", query, line, width)
			}
		}
	}
}

func newModel(t *testing.T, entries []projectpicker.Entry) *projectpicker.Model {
	t.Helper()

	model := projectpicker.NewModel(config.DefaultConfig(), entries)
	model.Update(tea.WindowSizeMsg{Width: 70, Height: 12})

	return model
}

func send(model *projectpicker.Model, messages ...tea.Msg) {
	for _, msg := range messages {
		model.Update(msg)
	}
}

func key(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func typed(text string) []tea.Msg {
	messages := make([]tea.Msg, 0, len(text))
	for _, r := range text {
		messages = append(messages, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}

	return messages
}

func plainView(model *projectpicker.Model) string {
	return ansi.Strip(model.View().Content)
}

func listLines(t *testing.T, model *projectpicker.Model) []string {
	t.Helper()

	lines := strings.Split(plainView(model), "\n")
	if len(lines) < 4 {
		t.Fatalf("view has %d lines", len(lines))
	}

	return lines[2 : len(lines)-2]
}
