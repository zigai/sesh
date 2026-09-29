package tui

import (
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
)

const maxSelectionKeySpecs = 4

type keyPressSpec string

type actionKeyMap struct {
	create     bubbleskey.Binding
	rename     bubbleskey.Binding
	kill       bubbleskey.Binding
	delete     bubbleskey.Binding
	snapshot   bubbleskey.Binding
	help       bubbleskey.Binding
	cycleMode  bubbleskey.Binding
	agentSort  bubbleskey.Binding
	pageUp     bubbleskey.Binding
	pageDown   bubbleskey.Binding
	top        bubbleskey.Binding
	bottom     bubbleskey.Binding
	selectKeys []bubbleskey.Binding
	enter      bubbleskey.Binding
	close      bubbleskey.Binding
	handoff    bubbleskey.Binding
	stepUp     bubbleskey.Binding
	stepDown   bubbleskey.Binding
	groupUp    bubbleskey.Binding
	groupDown  bubbleskey.Binding
}

func (k keyPressSpec) String() string {
	return string(k)
}

func newActionKeyMap(keys config.KeyConfig) actionKeyMap {
	return actionKeyMap{
		create:     newActionBinding(keys.Create, "Create session from search"),
		rename:     newActionBinding(keys.Rename, "Rename selected session"),
		kill:       newActionBinding(keys.Kill, "Kill detached selected session"),
		delete:     newActionBinding(keys.Delete, "Delete selected session everywhere"),
		snapshot:   newActionBinding(keys.Snapshot, "Snapshot selected remote server"),
		help:       newActionBinding(keys.Help, "Show this help"),
		cycleMode:  newActionBinding(keys.CycleMode, "Cycle display mode"),
		agentSort:  newActionBinding(keys.AgentSort, "Cycle agent sort order"),
		pageUp:     newActionBinding(keys.PageUp, "Page up"),
		pageDown:   newActionBinding(keys.PageDown, "Page down"),
		top:        newActionBinding(keys.Top, "Jump to top"),
		bottom:     newActionBinding(keys.Bottom, "Jump to bottom"),
		selectKeys: newSelectionBindings(keys),
		enter:      newActionBinding("enter", "Open selected session or agent"),
		close: newActionBindingSet(
			[]string{"esc", "ctrl+c", "ctrl+o"},
			"esc, ctrl+c, ctrl+o",
			"Close popup",
		),
		handoff: newActionBindingSet(
			[]string{"ctrl+p"},
			"ctrl+p",
			"Open floating terminal",
		),
		stepUp: newActionBindingSet(
			[]string{"up"},
			"↑",
			"Move selection up",
		),
		stepDown: newActionBindingSet(
			[]string{"down", "ctrl+n"},
			"↓/ctrl+n",
			"Move selection down",
		),
		groupUp: newActionBindingSet(
			[]string{"ctrl+up"},
			"ctrl+↑",
			"Move to previous tmux session",
		),
		groupDown: newActionBindingSet(
			[]string{"ctrl+down"},
			"ctrl+↓",
			"Move to next tmux session",
		),
	}
}

func newSelectionBindings(keys config.KeyConfig) []bubbleskey.Binding {
	specs := []struct {
		configured string
		digit      string
	}{
		{keys.Select1, "1"},
		{keys.Select2, "2"},
		{keys.Select3, "3"},
		{keys.Select4, "4"},
		{keys.Select5, "5"},
		{keys.Select6, "6"},
		{keys.Select7, "7"},
		{keys.Select8, "8"},
		{keys.Select9, "9"},
		{keys.Select0, "0"},
	}

	bindings := make([]bubbleskey.Binding, 0, len(specs))
	for _, spec := range specs {
		label := strings.TrimSpace(spec.configured)
		if label == "" {
			label = "ctrl+" + spec.digit
		}

		specList := selectionKeySpecs(spec.configured, spec.digit)
		bindings = append(bindings, newActionBindingSet(specList, label, "Open visible session or agent by slot"))
	}

	return bindings
}

func selectionKeySpecs(configured string, digit string) []string {
	configured = strings.TrimSpace(configured)

	set := make([]string, 0, maxSelectionKeySpecs)
	if configured != "" {
		set = append(set, configured)
	}

	altKey := "alt+" + digit
	ctrlKey := "ctrl+" + digit

	if !slices.Contains(set, altKey) {
		set = append(set, altKey)
	}

	if !slices.Contains(set, ctrlKey) {
		set = append(set, ctrlKey)
	}

	return set
}

func newActionBinding(spec string, description string) bubbleskey.Binding {
	return newActionBindingSet([]string{spec}, strings.TrimSpace(spec), description)
}

func newActionBindingSet(specs []string, label string, description string) bubbleskey.Binding {
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		if normalized := normalizeKeySpec(spec); normalized != "" {
			keys = append(keys, normalized)
		}
	}

	binding := bubbleskey.NewBinding(
		bubbleskey.WithKeys(keys...),
		bubbleskey.WithHelp(label, description),
	)
	if len(keys) == 0 {
		binding.Unbind()
	}

	return binding
}

func keyPressFromMsg(msg tea.KeyPressMsg) keyPressSpec {
	value := msg.String()
	if msg.Key().Mod != 0 {
		value = msg.Keystroke()
	}

	return keyPressSpec(normalizeKeySpec(value))
}

func normalizeKeySpec(value string) string {
	value = strings.TrimSpace(value)

	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "alt+shift+") && len([]rune(lower)) == len("alt+shift+")+1 {
		runes := []rune(lower)
		return "alt+" + strings.ToUpper(string(runes[len(runes)-1]))
	}

	if strings.HasPrefix(value, "alt+") && len([]rune(value)) == len("alt+")+1 {
		runes := []rune(value)

		last := string(runes[len(runes)-1])
		if last != strings.ToLower(last) {
			return "alt+" + last
		}
	}

	if !strings.Contains(value, "+") {
		return value
	}

	return lower
}
