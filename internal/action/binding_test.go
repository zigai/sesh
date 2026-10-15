package action_test

import (
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
)

func configWithBinds(binds ...config.KeyBindConfig) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Keys.Bind = binds

	return cfg
}

func bind(in, key, id string, params map[string]any) config.KeyBindConfig {
	return config.KeyBindConfig{Key: key, Action: id, In: in, Table: "", Params: params}
}

func boundTo(t *testing.T, bindings []action.Binding, in action.Surface, key string) (action.Binding, bool) {
	t.Helper()

	for _, binding := range bindings {
		if binding.In == in && binding.Key == key {
			return binding, true
		}
	}

	return action.Binding{}, false
}

func TestConfiguredBindingReplacesTheDefaultOnItsKey(t *testing.T) {
	t.Parallel()

	bindings, err := action.Bindings(configWithBinds(
		bind("picker", "alt+c", "session.rename", nil),
		bind("tmux", "F24", "agent.latest", nil),
		bind("tmux", "S-M-F12", "agent.back", nil),
	))
	if err != nil {
		t.Fatal(err)
	}

	if binding, ok := boundTo(t, bindings, action.SurfacePicker, "alt+c"); !ok || binding.Action != action.SessionRename.ID {
		t.Fatalf("alt+c = %#v, want the configured action", binding)
	}

	if binding, ok := boundTo(t, bindings, action.SurfaceTmux, "S-F12"); !ok || binding.Action != action.AgentLatest.ID {
		t.Fatalf("S-F12 written as F24 = %#v, want the configured action", binding)
	}

	if binding, ok := boundTo(t, bindings, action.SurfaceTmux, "M-S-F12"); !ok || binding.Action != action.AgentBack.ID {
		t.Fatalf("M-S-F12 written as S-M-F12 = %#v, want the configured action", binding)
	}

	if binding, ok := boundTo(t, bindings, action.SurfacePicker, "alt+r"); !ok || binding.Action != action.SessionRename.ID {
		t.Fatalf("untouched default alt+r = %#v", binding)
	}
}

func TestBindingWithoutActionRemovesTheDefault(t *testing.T) {
	t.Parallel()

	bindings, err := action.Bindings(configWithBinds(bind("picker", "alt+shift+x", "", nil)))
	if err != nil {
		t.Fatal(err)
	}

	if binding, ok := boundTo(t, bindings, action.SurfacePicker, "alt+X"); ok {
		t.Fatalf("unbound key still runs %s", binding.Action)
	}
}

func TestBindingsRejectBindingsTheirSurfaceCannotRun(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		binds []config.KeyBindConfig
		want  error
	}{
		"unknown action":          {[]config.KeyBindConfig{bind("picker", "ctrl+g", "session.explode", nil)}, action.ErrUnknownConfiguredAction},
		"unknown surface":         {[]config.KeyBindConfig{bind("shell", "ctrl+g", "session.create", nil)}, action.ErrInvalidBinding},
		"missing key":             {[]config.KeyBindConfig{bind("picker", " ", "session.create", nil)}, action.ErrInvalidBinding},
		"picker table":            {[]config.KeyBindConfig{{Key: "ctrl+g", Action: "session.create", In: "picker", Table: "root", Params: nil}}, action.ErrInvalidBinding},
		"client action in picker": {[]config.KeyBindConfig{bind("picker", "ctrl+g", "agent.next", nil)}, action.ErrBindingUnsupported},
		"tmux cannot prompt":      {[]config.KeyBindConfig{bind("tmux", "M-c", "session.create", nil)}, action.ErrMissingParam},
		"unknown param":           {[]config.KeyBindConfig{bind("tmux", "M-n", "agent.next", map[string]any{"global": true})}, action.ErrUnknownParam},
		"invalid param value":     {[]config.KeyBindConfig{bind("tmux", "M-n", "agent.next", map[string]any{"local": "sometimes"})}, action.ErrInvalidParam},
		"key bound twice":         {[]config.KeyBindConfig{bind("tmux", "M-n", "agent.next", nil), bind("tmux", "M-n", "agent.back", nil)}, action.ErrDuplicateBinding},
		"tmux key spelled twice":  {[]config.KeyBindConfig{bind("tmux", "M-C-x", "agent.next", nil), bind("tmux", "C-M-x", "agent.back", nil)}, action.ErrDuplicateBinding},
		"picker key spelled twice": {
			[]config.KeyBindConfig{bind("picker", "alt+ctrl+g", "session.create", nil), bind("picker", "ctrl+alt+g", "session.rename", nil)},
			action.ErrDuplicateBinding,
		},
		"configured picker key":   {[]config.KeyBindConfig{bind("picker", "?", "session.create", nil)}, action.ErrPickerKeyReserved},
		"fixed picker key":        {[]config.KeyBindConfig{bind("picker", "ctrl+n", "session.create", nil)}, action.ErrPickerKeyReserved},
		"slot key":                {[]config.KeyBindConfig{bind("picker", "alt+1", "session.create", nil)}, action.ErrPickerKeyReserved},
		"key types into search":   {[]config.KeyBindConfig{bind("picker", "x", "session.create", nil)}, action.ErrPickerKeyReserved},
		"space types into search": {[]config.KeyBindConfig{bind("picker", "space", "session.create", nil)}, action.ErrPickerKeyReserved},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := action.Bindings(configWithBinds(test.binds...)); !errors.Is(err, test.want) {
				t.Fatalf("Bindings() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPickerBindingRejectsConfiguredSessionKey(t *testing.T) {
	t.Parallel()

	cfg := configWithBinds(bind("picker", "alt+w", "session.create", nil))
	cfg.Keys.Sessions = map[string]string{"web": "w"}

	if _, err := action.Bindings(cfg); !errors.Is(err, action.ErrPickerKeyReserved) {
		t.Fatalf("Bindings() error = %v, want %v", err, action.ErrPickerKeyReserved)
	}
}

func TestTmuxBindingCarriesItsParams(t *testing.T) {
	t.Parallel()

	bindings, err := action.Bindings(configWithBinds(bind("tmux", "M-c", "session.create", map[string]any{"name": "scratch", "dir": "~/work"})))
	if err != nil {
		t.Fatal(err)
	}

	binding, ok := boundTo(t, bindings, action.SurfaceTmux, "M-c")
	if !ok || binding.Table != action.DefaultTmuxTable || binding.Values.String("name") != "scratch" || binding.Values.String("dir") != "~/work" {
		t.Fatalf("M-c = %#v, want session.create in the root table with its params", binding)
	}
}
