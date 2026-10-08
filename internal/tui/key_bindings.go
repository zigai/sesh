package tui

import (
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
)

type keyPressSpec string

type actionKeyMap struct {
	actions    []boundAction
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

type boundAction struct {
	key     bubbleskey.Binding
	binding action.Binding
}

func (k keyPressSpec) String() string {
	return string(k)
}

func newActionKeyMap(cfg *config.Config) actionKeyMap {
	keys := cfg.Keys

	return actionKeyMap{
		actions:    pickerActions(cfg),
		help:       newActionBinding(keys.Help, "Show this help"),
		cycleMode:  newActionBinding(keys.CycleMode, "Cycle display mode"),
		agentSort:  newActionBinding(keys.AgentSort, "Cycle agent sort order"),
		pageUp:     newActionBinding(keys.PageUp, "Page up"),
		pageDown:   newActionBinding(keys.PageDown, "Page down"),
		top:        newActionBinding(keys.Top, "Jump to top"),
		bottom:     newActionBinding(keys.Bottom, "Jump to bottom"),
		selectKeys: newSelectionBindings(keys),
		enter:      newActionBindingSet(config.PickerSelectKeys, "enter", "Open selected session or agent"),
		close:      newActionBindingSet(config.PickerCloseKeys, "esc, ctrl+c, ctrl+o", "Close popup"),
		handoff:    newActionBindingSet(config.PickerHandoffKeys, "ctrl+p", "Open floating terminal"),
		stepUp:     newActionBindingSet(config.PickerStepUpKeys, "↑", "Move selection up"),
		stepDown:   newActionBindingSet(config.PickerStepDownKeys, "↓/ctrl+n", "Move selection down"),
		groupUp:    newActionBindingSet(config.PickerGroupUpKeys, "ctrl+↑", "Move to previous tmux session"),
		groupDown:  newActionBindingSet(config.PickerGroupDownKeys, "ctrl+↓", "Move to next tmux session"),
	}
}

// pickerActions returns the action bindings pressed in the picker. Configuration is validated
// when it is loaded, so a configuration that fails to resolve here binds no actions.
func pickerActions(cfg *config.Config) []boundAction {
	bindings, err := action.Bindings(cfg)
	if err != nil {
		return nil
	}

	bound := make([]boundAction, 0, len(bindings))

	for _, binding := range action.SurfaceBindings(bindings, action.SurfacePicker) {
		entry, ok := action.Lookup(binding.Action)
		if !ok {
			continue
		}

		bound = append(bound, boundAction{key: newActionBinding(binding.Key, entry.Describe().Title), binding: binding})
	}

	return bound
}

// availableActions returns the picker's action bindings that apply to the selection.
func (m *Model) availableActions(keys actionKeyMap) []boundAction {
	available := make([]boundAction, 0, len(keys.actions))

	for _, bound := range keys.actions {
		entry, ok := action.Lookup(bound.binding.Action)
		if !ok {
			continue
		}

		descriptor := entry.Describe()

		subject, ok := m.actionSubject(descriptor)
		if ok && m.actionAvailable(descriptor, subject) == nil {
			available = append(available, bound)
		}
	}

	return available
}

// actionKey returns the key bound to id without parameters, or empty when none is.
func (k actionKeyMap) actionKey(id action.ID) string {
	for _, bound := range k.actions {
		if bound.binding.Action == id && len(bound.binding.Values) == 0 {
			return bound.key.Help().Key
		}
	}

	return ""
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

		bindings = append(bindings, newActionBindingSet(config.PickerSlotKeys(spec.configured, spec.digit), label, "Open visible session or agent by slot"))
	}

	return bindings
}

func newActionBinding(spec string, description string) bubbleskey.Binding {
	return newActionBindingSet([]string{spec}, strings.TrimSpace(spec), description)
}

func newActionBindingSet(specs []string, label string, description string) bubbleskey.Binding {
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		if normalized := config.PickerKeyName(spec); normalized != "" {
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

	return keyPressSpec(config.PickerKeyName(value))
}
