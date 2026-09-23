package config_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestSavePreservesShellAndArgumentHooks(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Hooks.SessionSwitchPre = []config.HookCommand{
		{ShellCommand: "printf 'first\\nsecond'\n# literal comment"},
		{Command: "/bin/echo", Args: []string{"", "space value", "quote\"value"}, Timeout: "2s"},
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := config.Save(cfg, config.WithSavePath(path)); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(config.WithExplicitPath(path))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(loaded.Hooks.SessionSwitchPre, cfg.Hooks.SessionSwitchPre) {
		t.Fatalf("hook commands changed: %#v", loaded.Hooks)
	}
}

func TestSavePreservesResolvedSparseStatus(t *testing.T) {
	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader("[agent_sessions.status]\nstyle_waiting = 'fg=red'\n")))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := config.Save(cfg, config.WithSavePath(path)); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(config.WithExplicitPath(path))
	if err != nil {
		t.Fatal(err)
	}

	status := loaded.ResolvedAgentStatus()
	if !status.Enabled || !status.ActiveBypass || !status.ClearOnShutdown || status.StyleWaiting != "fg=red" {
		t.Fatalf("sparse status changed: %#v", status)
	}
}

func TestSavePreservesSnapshotProcessEnvironment(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{name: "default", env: config.DefaultProcessEnvironment},
		{name: "empty", env: []string{}},
		{name: "custom", env: []string{"MY_VAR", "VIRTUAL_ENV"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Snapshot.ProcessEnvironment = tc.env

			path := filepath.Join(t.TempDir(), "config.toml")
			if _, err := config.Save(cfg, config.WithSavePath(path)); err != nil {
				t.Fatal(err)
			}

			loaded, err := config.Load(config.WithExplicitPath(path))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(loaded.Snapshot.ProcessEnvironment, tc.env) {
				t.Fatalf("ProcessEnvironment changed: got %#v, want %#v", loaded.Snapshot.ProcessEnvironment, tc.env)
			}
		})
	}
}
