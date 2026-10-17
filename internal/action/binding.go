package action

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

const (
	SurfacePicker Surface = "picker"
	SurfaceTmux   Surface = "tmux"

	DefaultTmuxTable = "root"
)

var (
	ErrInvalidBinding     = errors.New("invalid key binding")
	ErrDuplicateBinding   = errors.New("key is bound twice")
	ErrBindingUnsupported = errors.New("action cannot be bound here")
	ErrPickerKeyReserved  = errors.New("key is reserved by the picker")
)

// Surface is where a key binding is pressed: inside the picker, or in a tmux key table.
type Surface string

// Binding runs an action when its key is pressed. Values are parameter values the binding
// supplies; the picker prompts for the rest, while a tmux binding must supply every required one.
type Binding struct {
	Key    string
	Action ID
	In     Surface
	Table  string
	Values Values
}

// Bindings resolves the configured key bindings over the defaults. A configured binding replaces
// the default bound to the same key, and one without an action removes it.
func Bindings(cfg *config.Config) ([]Binding, error) {
	bindings := defaultBindings()
	if cfg == nil {
		return bindings, nil
	}

	configured := make([]Binding, 0, len(cfg.Keys.Bind))

	for index, bind := range cfg.Keys.Bind {
		binding, err := configuredBinding(bind)
		if err != nil {
			return nil, oops.In("action").Code("config_invalid").With("operation", "resolve_bindings", "binding", index+1).Wrap(err)
		}

		if slices.ContainsFunc(configured, binding.sameKey) {
			return nil, oops.In("action").Code("config_invalid").With("operation", "resolve_bindings", "binding", index+1).
				Wrap(fmt.Errorf("%w: %s", ErrDuplicateBinding, binding.Key))
		}

		configured = append(configured, binding)
	}

	for _, binding := range configured {
		bindings = slices.DeleteFunc(bindings, binding.sameKey)
		if binding.Action != "" {
			bindings = append(bindings, binding)
		}
	}

	bindings = append(bindings, taskActionBindings(cfg, bindings)...)

	if err := checkPickerKeys(cfg.Keys, bindings); err != nil {
		return nil, err
	}

	return bindings, nil
}

// SurfaceBindings returns the bindings pressed on surface.
func SurfaceBindings(bindings []Binding, surface Surface) []Binding {
	return slices.DeleteFunc(slices.Clone(bindings), func(binding Binding) bool { return binding.In != surface })
}

func defaultBindings() []Binding {
	picker := func(key string, definition Entry) Binding {
		return Binding{Key: key, Action: definition.Describe().ID, In: SurfacePicker, Table: "", Values: Values{}}
	}

	tmux := func(key string, definition Entry, values Values) Binding {
		return Binding{Key: key, Action: definition.Describe().ID, In: SurfaceTmux, Table: DefaultTmuxTable, Values: values}
	}

	return []Binding{
		picker("alt+c", SessionCreate),
		picker("alt+r", SessionRename),
		picker("alt+x", SessionKill),
		picker(config.PickerKeyName("alt+shift+x"), SessionDelete),
		picker("alt+s", SnapshotSave),
		picker("alt+z", TaskSleep),
		picker(config.PickerKeyName("alt+shift+z"), TaskWake),
		picker("alt+f", TaskFinish),
		picker("alt+o", TaskOpen),
		tmux("S-F12", AgentNext, Values{}),
		tmux("M-S-F12", AgentNext, Values{"local": {"true"}}),
		tmux("S-F1", AgentBack, Values{}),
		tmux("C-o", PickPopup, Values{}),
		tmux("M-w", PickPopup, Values{"mode": {"inbox"}}),
		tmux("C-t", TaskMenu, Values{}),
		tmux("M-n", ProjectPick, Values{}),
	}
}

// taskActionBindings binds the picker keys of configured task actions to task.run. A key that an
// action binding already uses stays with that action.
func taskActionBindings(cfg *config.Config, existing []Binding) []Binding {
	keys := make(map[string]string)

	for name, configured := range cfg.Menu.Items {
		if configured.Key != "" {
			keys[config.PickerKeyName(configured.Key)] = name
		}
	}

	for _, project := range cfg.Projects.Overrides {
		for name, configured := range project.MenuItems {
			if configured.Key != "" {
				keys[config.PickerKeyName(configured.Key)] = name
			}
		}
	}

	bindings := make([]Binding, 0, len(keys))

	for _, key := range slices.Sorted(maps.Keys(keys)) {
		binding := Binding{Key: key, Action: TaskRun.ID, In: SurfacePicker, Table: "", Values: Values{"action": {keys[key]}}}
		if !slices.ContainsFunc(existing, binding.sameKey) {
			bindings = append(bindings, binding)
		}
	}

	return bindings
}

func (binding Binding) sameKey(other Binding) bool {
	return binding.In == other.In && binding.Table == other.Table && binding.Key == other.Key
}

func configuredBinding(bind config.KeyBindConfig) (Binding, error) {
	binding := Binding{Key: "", Action: ID(bind.Action), In: Surface(bind.In), Table: bind.Table, Values: Values{}}

	switch binding.In {
	case SurfacePicker:
		binding.Key = config.PickerKeyName(bind.Key)

		if binding.Table != "" {
			return binding, fmt.Errorf("%w: picker bindings have no key table", ErrInvalidBinding)
		}
	case SurfaceTmux:
		binding.Key = config.TmuxKeyName(bind.Key)

		if binding.Table == "" {
			binding.Table = DefaultTmuxTable
		}
	default:
		return binding, fmt.Errorf("%w: in must be picker or tmux", ErrInvalidBinding)
	}

	if binding.Key == "" {
		return binding, fmt.Errorf("%w: key is required", ErrInvalidBinding)
	}

	if binding.Action == "" {
		if len(bind.Params) > 0 {
			return binding, fmt.Errorf("%w: params need an action", ErrInvalidBinding)
		}

		return binding, nil
	}

	entry, ok := Lookup(binding.Action)
	if !ok {
		return binding, fmt.Errorf("%w: %s", ErrUnknownConfiguredAction, binding.Action)
	}

	values, err := bindingValues(bind.Params)
	if err != nil {
		return binding, err
	}

	binding.Values = values

	return binding, checkBindable(entry.Describe(), binding)
}

// checkBindable rejects actions a surface cannot run: the picker supplies a selected row but no
// tmux client, and a tmux key supplies the current session or pane but cannot prompt.
func checkBindable(descriptor Descriptor, binding Binding) error {
	switch binding.In {
	case SurfacePicker:
		if descriptor.NeedsClient || descriptor.Interactive {
			return fmt.Errorf("%w: %s needs a tmux client", ErrBindingUnsupported, descriptor.ID)
		}

		return checkValues(descriptor.Params, binding.Values, false)
	case SurfaceTmux:
		if descriptor.InPicker {
			return fmt.Errorf("%w: %s runs only in the picker", ErrBindingUnsupported, descriptor.ID)
		}

		if descriptor.Subject == SubjectSavedSession && descriptor.Arity != ArityOptional {
			return fmt.Errorf("%w: %s needs a saved session", ErrBindingUnsupported, descriptor.ID)
		}

		return checkValues(descriptor.Params, binding.Values, true)
	default:
		return fmt.Errorf("%w: in must be picker or tmux", ErrInvalidBinding)
	}
}

func checkValues(params []Param, values Values, complete bool) error {
	for name := range values {
		if !slices.ContainsFunc(params, func(param Param) bool { return param.Name == name }) {
			return fmt.Errorf("%w: %s", ErrUnknownParam, name)
		}
	}

	for _, param := range params {
		items, present := values[param.Name]
		if !present {
			if complete && param.Required && param.Default == "" {
				return fmt.Errorf("%w: %s", ErrMissingParam, param.Name)
			}

			continue
		}

		if _, err := param.check(items); err != nil {
			return err
		}
	}

	return nil
}

func bindingValues(params map[string]any) (Values, error) {
	values := make(Values, len(params))

	for name, raw := range params {
		switch value := raw.(type) {
		case string:
			values.Set(name, value)
		case bool:
			values.Set(name, strconv.FormatBool(value))
		case int64:
			values.Set(name, strconv.FormatInt(value, 10))
		case []any:
			items := make([]string, 0, len(value))
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("%w: %s", ErrInvalidParam, name)
				}

				items = append(items, text)
			}

			values.Set(name, items...)
		default:
			return nil, fmt.Errorf("%w: %s", ErrInvalidParam, name)
		}
	}

	return values, nil
}

// checkPickerKeys refuses picker bindings that could never run: keys the picker handles before
// action bindings, and keys that type into its search field.
func checkPickerKeys(keys config.KeyConfig, bindings []Binding) error {
	reserved := config.PickerReservedKeys(keys)

	for _, binding := range SurfaceBindings(bindings, SurfacePicker) {
		var err error

		switch {
		case slices.ContainsFunc(reserved, func(key string) bool { return key != "" && config.PickerKeyName(key) == binding.Key }):
			err = fmt.Errorf("%w: %s is a picker navigation key", ErrPickerKeyReserved, binding.Key)
		case config.PickerKeyTypes(binding.Key):
			err = fmt.Errorf("%w: %s types into the search field", ErrPickerKeyReserved, binding.Key)
		}

		if err != nil {
			return oops.In("action").Code("config_invalid").With("operation", "resolve_bindings").Wrap(err)
		}
	}

	return nil
}
