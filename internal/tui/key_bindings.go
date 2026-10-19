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
	inboxGroup bubbleskey.Binding
	menu       bubbleskey.Binding
	pageUp     bubbleskey.Binding
	pageDown   bubbleskey.Binding
	top        bubbleskey.Binding
	bottom     bubbleskey.Binding
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

func newActionKeyMap(cfg *config.Config, bindings []action.Binding) actionKeyMap {
	keys := cfg.Keys

	return actionKeyMap{
		actions:    pickerActions(bindings),
		help:       newActionBinding(keys.Help, "Show this help"),
		cycleMode:  newActionBinding(keys.CycleMode, "Cycle display mode"),
		agentSort:  newActionBinding(keys.AgentSort, "Cycle agent sort order"),
		inboxGroup: newActionBinding(keys.InboxGroup, "Group the inbox by state or project"),
		menu:       newActionBinding(keys.Menu, "Open the menu of the selected row"),
		pageUp:     newActionBinding(keys.PageUp, "Page up"),
		pageDown:   newActionBinding(keys.PageDown, "Page down"),
		top:        newActionBinding(keys.Top, "Jump to top"),
		bottom:     newActionBinding(keys.Bottom, "Jump to bottom"),
		enter:      newActionBindingSet(config.PickerSelectKeys, "enter", "Open selected session or agent"),
		close:      newActionBindingSet(config.PickerCloseKeys, "esc, ctrl+c, ctrl+o", "Close popup"),
		handoff:    newActionBindingSet(config.PickerHandoffKeys, "ctrl+p", "Open floating terminal"),
		stepUp:     newActionBindingSet(config.PickerStepUpKeys, "↑", "Move selection up"),
		stepDown:   newActionBindingSet(config.PickerStepDownKeys, "↓/ctrl+n", "Move selection down"),
		groupUp:    newActionBindingSet(config.PickerGroupUpKeys, "ctrl+↑", "Move to previous tmux session"),
		groupDown:  newActionBindingSet(config.PickerGroupDownKeys, "ctrl+↓", "Move to next tmux session"),
	}
}

func pickerActions(bindings []action.Binding) []boundAction {
	bound := make([]boundAction, 0, len(bindings))

	for _, binding := range action.SurfaceBindings(bindings, action.SurfacePicker) {
		entry, ok := action.Lookup(binding.Action)
		if !ok {
			continue
		}

		description := entry.Describe().Title
		if name := binding.Values.String("action"); binding.Action == action.TaskRun.ID && name != "" {
			description = name
		}

		bound = append(bound, boundAction{key: newActionBinding(binding.Key, description), binding: binding})
	}

	return bound
}

func (m *Model) actionKeys() actionKeyMap {
	return newActionKeyMap(m.config, m.actionBindings)
}

func (m *Model) availableActions(keys actionKeyMap) []boundAction {
	available := make([]boundAction, 0, len(keys.actions))

	for _, bound := range keys.actions {
		entry, ok := action.Lookup(bound.binding.Action)
		if !ok {
			continue
		}

		descriptor := entry.Describe()
		if descriptor.InPicker {
			if m.pickerActionError(descriptor.ID) == nil {
				available = append(available, bound)
			}

			continue
		}

		subject, ok := m.actionSubject(descriptor)
		if ok && m.actionAvailable(descriptor, subject) == nil {
			available = append(available, bound)
		}
	}

	return available
}

func (k actionKeyMap) actionKey(id action.ID) string {
	for _, bound := range k.actions {
		if bound.binding.Action == id && len(bound.binding.Values) == 0 {
			return bound.key.Help().Key
		}
	}

	return ""
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
