package config_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

func TestConfigPrecedenceAcrossUserEnvironmentAndExplicitPathIgnoresWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	project := filepath.Join(root, "project")

	if err := os.MkdirAll(filepath.Join(configHome, "sesh"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	userPath := filepath.Join(configHome, "sesh", "config.toml")
	projectPath := filepath.Join(project, ".sesh.toml")
	explicitPath := filepath.Join(root, "explicit.toml")

	for path, timeout := range map[string]string{userPath: "11s", projectPath: "13s", explicitPath: "19s"} {
		if err := os.WriteFile(path, []byte("[hooks]\ntimeout = \""+timeout+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("SESH_CONFIG", "")
	t.Chdir(project)

	check := func(want string, opts ...config.LoadOption) {
		t.Helper()

		cfg, err := config.Load(opts...)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Hooks.Timeout != want {
			t.Fatalf("hook timeout = %q, want %q", cfg.Hooks.Timeout, want)
		}
	}

	check("11s")
	t.Setenv("SESH_HOOKS_TIMEOUT", "17s")
	check("17s")

	if err := os.Unsetenv("SESH_HOOKS_TIMEOUT"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SESH_CONFIG", userPath)
	check("11s")
	check("19s", config.WithExplicitPath(explicitPath))
	check("30s", config.WithoutConfig())
}

func TestLoadedConfigPreservesItsSourceAcrossDirectoryChangesAndCloning(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(root, "selected.toml")
	if err := os.WriteFile(path, []byte("[tasks]\npopup_socket = 'selected'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	t.Setenv("SESH_CONFIG", path)

	input := "[tasks]\npopup_socket = 'stdin'\n"

	for _, test := range []struct {
		options []config.LoadOption
		source  config.Source
	}{
		{[]config.LoadOption{config.WithExplicitPath("selected.toml")}, config.Source{Path: path}},
		{nil, config.Source{Path: path}},
		{[]config.LoadOption{config.WithoutConfig()}, config.Source{WithoutConfig: true}},
		{[]config.LoadOption{config.WithExplicitPath("-"), config.WithInput(strings.NewReader(input))}, config.Source{Path: "-", Input: input}},
	} {
		cfg, err := config.Load(test.options...)
		if err != nil {
			t.Fatal(err)
		}

		t.Chdir(t.TempDir())

		if source := cfg.Clone().Source(); source != test.source {
			t.Fatalf("cloned config source = %+v, want %+v", source, test.source)
		}

		t.Chdir(root)
	}
}

func TestLoadDecodesDisplayFieldCommands(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("SESH_CONFIG", "")
	t.Setenv("SESH_DISPLAY_SESH_FORMAT", "{session_name}{right}{custom.weather}")

	if err := os.MkdirAll(filepath.Join(configHome, "sesh"), 0o700); err != nil {
		t.Fatal(err)
	}

	source := `[display]
sesh_format = "{session_name} {custom.sha}"

[display.fields]
sha = "git rev-parse --short HEAD"
weather = { command = "/opt/bin/weather", args = ["--short", "x y"], refresh = "10m" }
`
	if err := os.WriteFile(filepath.Join(configHome, "sesh", "config.toml"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	want := config.FieldCommands{
		"sha":     {ShellCommand: "git rev-parse --short HEAD", Command: "", Args: nil, Refresh: ""},
		"weather": {ShellCommand: "", Command: "/opt/bin/weather", Args: []string{"--short", "x y"}, Refresh: "10m"},
	}
	if !reflect.DeepEqual(cfg.Display.Fields, want) {
		t.Fatalf("Display.Fields = %#v, want %#v", cfg.Display.Fields, want)
	}

	if cfg.Display.SeshFormat != "{session_name}{right}{custom.weather}" {
		t.Fatalf("SeshFormat = %q, want environment override", cfg.Display.SeshFormat)
	}

	cloned := cfg.Clone()
	cloned.Display.Fields["weather"].Args[0] = "--long"

	if cfg.Display.Fields["weather"].Args[0] != "--short" {
		t.Fatal("Clone() shares field args with the original")
	}
}

func TestRemoteConfigTOMLDecodesConnections(t *testing.T) {
	cfg := config.DefaultConfig()
	source := `[remote]
enabled = true
ssh_path = "/usr/bin/ssh"
connections = [
  { id = "work", label = "Work", host = "work.example.com", sesh_path = "/usr/local/bin/sesh", tmux_socket = "work" },
]`

	d := toml.NewDecoder(strings.NewReader(source))
	d.DisallowUnknownFields()

	if err := d.Decode(cfg); err != nil {
		t.Fatalf("decoding remote config: %v", err)
	}

	if !cfg.Remote.Enabled || cfg.Remote.SSHPath != "/usr/bin/ssh" {
		t.Fatalf("Remote = %#v, want enabled with overridden SSH path", cfg.Remote)
	}

	if len(cfg.Remote.Connections) != 1 {
		t.Fatalf("Connections = %#v, want one connection", cfg.Remote.Connections)
	}

	connection := cfg.Remote.Connections[0]
	if connection.ID != "work" || connection.Label != "Work" ||
		connection.Host != "work.example.com" ||
		connection.SeshPath != "/usr/local/bin/sesh" ||
		connection.TmuxSocket != "work" {
		t.Fatalf("Connection = %#v, want decoded remote settings", connection)
	}
}

func TestLoadOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[theme]
primary = "#ff0000"
warning = "#ff8800"

[popup]
sesh_width = "42%"
sesh_height = "48%"
agents_width = "55%"
agents_height = "75%"

[keys]
help = "f1"
cycle_mode = "ctrl+e"
page_up = "ctrl+b"
page_down = "ctrl+f"
top = "g"
bottom = "G"

[[keys.bind]]
key = "M-n"
action = "agent.next"
in = "tmux"
params = { local = true }`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	assertEqual(t, "Primary theme", cfg.Theme.Primary, "#ff0000")
	assertEqual(t, "Warning theme", cfg.Theme.Warning, "#ff8800")

	if len(cfg.Keys.Bind) != 1 || cfg.Keys.Bind[0].Key != "M-n" || cfg.Keys.Bind[0].Action != "agent.next" ||
		cfg.Keys.Bind[0].In != "tmux" || cfg.Keys.Bind[0].Params["local"] != true {
		t.Fatalf("Keys.Bind = %#v, want the configured tmux binding", cfg.Keys.Bind)
	}

	assertEqual(t, "Help key", cfg.Keys.Help, "f1")
	assertEqual(t, "CycleMode key", cfg.Keys.CycleMode, "ctrl+e")
	assertEqual(t, "SESH popup width", cfg.Popup.SeshWidth, "42%")
	assertEqual(t, "SESH popup height", cfg.Popup.SeshHeight, "48%")
	assertEqual(t, "AGENTS popup width", cfg.Popup.AgentsWidth, "55%")
	assertEqual(t, "AGENTS popup height", cfg.Popup.AgentsHeight, "75%")
	assertEqual(t, "PageUp key", cfg.Keys.PageUp, "ctrl+b")
	assertEqual(t, "PageDown key", cfg.Keys.PageDown, "ctrl+f")
	assertEqual(t, "Top key", cfg.Keys.Top, "g")
	assertEqual(t, "Bottom key", cfg.Keys.Bottom, "G")
}

func TestLoadSettingsOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[settings]
show_footer = false
directory_suggestions = 8
default_session_path_suggestion = "~/src/{session}"
display_modes = ["agents"]`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Settings.ShowFooter {
		t.Fatalf("settings = %#v, want footer disabled", cfg.Settings)
	}

	assertEqual(t, "DirectorySuggestions setting", cfg.Settings.DirectorySuggestions, 8)
	assertEqual(t, "DefaultSessionPathSuggestion setting", cfg.Settings.DefaultSessionPathSuggestion, "~/src/{session}")

	if !slices.Equal(cfg.Settings.DisplayModes, []string{"agents"}) {
		t.Fatalf("DisplayModes = %#v, want agents", cfg.Settings.DisplayModes)
	}
}

func TestLoadDisplayOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[display]
sesh_format = "{session_name} ({window_count})"
agents_session_format = "{agent_status}: {session_name}"
agents_agent_format = "{status} {harness_raw} {agent_session}"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	assertEqual(t, "SESH display format", cfg.Display.SeshFormat, "{session_name} ({window_count})")
	assertEqual(t, "AGENTS session format", cfg.Display.AgentsSessionFormat, "{agent_status}: {session_name}")
	assertEqual(t, "AGENTS agent format", cfg.Display.AgentsAgentFormat, "{status} {harness_raw} {agent_session}")
}

func TestLoadHooksOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[hooks]
timeout = "10s"
session_switch_pre = [
  "echo pre",
  { command = "notify-send", args = ["switching"], timeout = "2s" },
]
tmux_startup_post = [
  "echo startup",
]`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	assertEqual(t, "Hook timeout", cfg.Hooks.Timeout, "10s")

	if len(cfg.Hooks.SessionSwitchPre) != 2 {
		t.Fatalf("SessionSwitchPre len = %d, want 2", len(cfg.Hooks.SessionSwitchPre))
	}

	assertEqual(t, "Shell hook", cfg.Hooks.SessionSwitchPre[0].ShellCommand, "echo pre")
	assertEqual(t, "Argv hook command", cfg.Hooks.SessionSwitchPre[1].Command, "notify-send")
	assertEqual(t, "Argv hook arg", cfg.Hooks.SessionSwitchPre[1].Args[0], "switching")
	assertEqual(t, "Argv hook timeout", cfg.Hooks.SessionSwitchPre[1].Timeout, "2s")
	assertEqual(t, "Startup hook", cfg.Hooks.TmuxStartupPost[0].ShellCommand, "echo startup")
}

func TestLoadAgentSessionsOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[agent_sessions]
enabled = false
popup_counts = false
refresh_interval = "5s"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	assertEqual(t, "AgentSessions enabled", cfg.AgentSessions.Enabled, false)
	assertEqual(t, "AgentSessions popup counts", cfg.AgentSessions.PopupCounts, false)
	assertEqual(t, "AgentSessions refresh interval", cfg.AgentSessions.RefreshInterval, "5s")
}

func TestGeneratedConfigDefaultsMatchBuiltIns(t *testing.T) {
	data, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatalf("reading generated config template: %v", err)
	}

	data = uncommentDefaultSettings(data)

	var generated config.Config

	d := toml.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.EnableUnmarshalerInterface()

	if err := d.Decode(&generated); err != nil {
		t.Fatalf("parsing generated config defaults: %v", err)
	}

	generated.Hooks.SessionSwitchPre = nil
	generated.Hooks.SessionSwitchPost = nil
	generated.Hooks.SessionAttachPre = nil
	generated.Hooks.SessionAttachPost = nil
	generated.Hooks.TmuxStartupPost = nil

	got, err := toml.Marshal(&generated)
	if err != nil {
		t.Fatalf("marshaling generated config: %v", err)
	}

	want, err := toml.Marshal(config.DefaultConfig())
	if err != nil {
		t.Fatalf("marshaling default config: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("generated config does not match defaults\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func uncommentDefaultSettings(data []byte) []byte {
	lines := strings.Split(string(data), "\n")

	for index, line := range lines {
		setting, ok := strings.CutPrefix(line, "# ")
		if !ok || !isTOMLSetting(setting) {
			continue
		}

		lines[index] = setting
	}

	return []byte(strings.Join(lines, "\n"))
}

func isTOMLSetting(line string) bool {
	name, _, ok := strings.Cut(line, " = ")
	if !ok || name == "" {
		return false
	}

	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}

	return true
}

func TestLoadSnapshotProcessEnvironmentOverrides(t *testing.T) {
	t.Run("explicit empty disables environment capture", func(t *testing.T) {
		cfg, err := config.Load(
			config.WithExplicitPath("-"),
			config.WithInput(strings.NewReader("[snapshot]\nprocess_environment = []\n")),
		)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Snapshot.ProcessEnvironment == nil || len(cfg.Snapshot.ProcessEnvironment) != 0 {
			t.Fatalf("ProcessEnvironment = %#v, want empty slice []", cfg.Snapshot.ProcessEnvironment)
		}
	})

	t.Run("explicit custom keys", func(t *testing.T) {
		cfg, err := config.Load(
			config.WithExplicitPath("-"),
			config.WithInput(strings.NewReader("[snapshot]\nprocess_environment = [\"MY_CUSTOM_VAR\", \"VIRTUAL_ENV\"]\n")),
		)
		if err != nil {
			t.Fatal(err)
		}

		expected := []string{"MY_CUSTOM_VAR", "VIRTUAL_ENV"}
		if !slices.Equal(cfg.Snapshot.ProcessEnvironment, expected) {
			t.Fatalf("ProcessEnvironment = %#v, want %#v", cfg.Snapshot.ProcessEnvironment, expected)
		}
	})
}

func TestLoadIntoPreservesTargetOnFailure(t *testing.T) {
	t.Parallel()

	target := config.DefaultConfig()
	target.Theme.Primary = "#112233"

	tempDir := t.TempDir()

	badConfig := filepath.Join(tempDir, "broken.toml")
	if err := os.WriteFile(badConfig, []byte("invalid toml syntax [["), 0o600); err != nil {
		t.Fatal(err)
	}

	err := config.LoadInto(target, config.WithExplicitPath(badConfig))
	if err == nil {
		t.Fatal("LoadInto() error = nil, want syntax error")
	}

	if target.Theme.Primary != "#112233" {
		t.Fatalf("target was mutated on failure: Theme.Primary = %q, want #112233", target.Theme.Primary)
	}
}

func TestLoadRejectsParentDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realDir := filepath.Join(tempDir, "real_dir")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}

	configFile := filepath.Join(realDir, "config.toml")
	if err := os.WriteFile(configFile, []byte("[theme]\nprimary = \"#111111\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkDir := filepath.Join(tempDir, "symlink_dir")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	_, err := config.Load(config.WithExplicitPath(filepath.Join(symlinkDir, "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Load() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestLoadDecodesNonTomlExtensionAsToml(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	confPath := filepath.Join(tempDir, "myconfig.conf")
	if err := os.WriteFile(confPath, []byte("[theme]\nprimary = \"#445566\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(confPath))
	if err != nil {
		t.Fatalf("Load(myconfig.conf) error = %v", err)
	}

	if cfg.Theme.Primary != "#445566" {
		t.Errorf("Theme.Primary = %q, want #445566", cfg.Theme.Primary)
	}

	yamlPath := filepath.Join(tempDir, "myconfig.yaml")

	yamlContent := "theme:\n  primary: \"#112233\"\n"
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(config.WithExplicitPath(yamlPath))
	if err == nil {
		t.Fatal("Load(myconfig.yaml) unexpectedly succeeded, want TOML rejection for YAML syntax")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error: %v", err)
	}

	if oopsErr.Domain() != "config" {
		t.Errorf("Domain = %q, want config", oopsErr.Domain())
	}
}

func TestLoadAcceptsLeadingByteOrderMark(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf[theme]\nprimary = \"#445566\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(path))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Theme.Primary != "#445566" {
		t.Errorf("Theme.Primary = %q, want #445566", cfg.Theme.Primary)
	}
}

func TestConsistentEnvironmentPrefixAcrossAllModes(t *testing.T) {
	t.Setenv("SETTINGS_LOG_LEVEL", "debug")
	t.Setenv("LOG_LEVEL", "debug")

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[theme]\nprimary = \"#000000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgA, err := config.Load(config.WithoutConfig())
	if err != nil {
		t.Fatalf("Load(WithoutConfig) error = %v", err)
	}

	if cfgA.Settings.LogLevel != "info" {
		t.Errorf("Mode A LogLevel = %q, want default info (ambient env leaked)", cfgA.Settings.LogLevel)
	}

	cfgB, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load(WithExplicitPath) error = %v", err)
	}

	if cfgB.Settings.LogLevel != "info" {
		t.Errorf("Mode B LogLevel = %q, want default info (ambient env leaked)", cfgB.Settings.LogLevel)
	}

	t.Setenv("SESH_SETTINGS_LOG_LEVEL", "warn")

	cfgA2, err := config.Load(config.WithoutConfig())
	if err != nil {
		t.Fatalf("Load(WithoutConfig) error = %v", err)
	}

	if cfgA2.Settings.LogLevel != "warn" {
		t.Errorf("Mode A with SESH_ prefix LogLevel = %q, want warn", cfgA2.Settings.LogLevel)
	}

	cfgB2, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load(WithExplicitPath) error = %v", err)
	}

	if cfgB2.Settings.LogLevel != "warn" {
		t.Errorf("Mode B with SESH_ prefix LogLevel = %q, want warn", cfgB2.Settings.LogLevel)
	}
}

func TestLoadRejectsAncestorDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	realGrandparent := filepath.Join(tempDir, "real_gp")

	realParent := filepath.Join(realGrandparent, "nested")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatal(err)
	}

	configFile := filepath.Join(realParent, "config.toml")
	if err := os.WriteFile(configFile, []byte("[theme]\nprimary = \"#111111\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkGrandparent := filepath.Join(tempDir, "symlink_gp")
	if err := os.Symlink(realGrandparent, symlinkGrandparent); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	_, err := config.Load(config.WithExplicitPath(filepath.Join(symlinkGrandparent, "nested", "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Load() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestLoadWithInputCustomReader(t *testing.T) {
	t.Parallel()

	input := strings.NewReader("[theme]\nprimary = \"#abcdef\"\n")

	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(input))
	if err != nil {
		t.Fatalf("Load(WithInput) error = %v", err)
	}

	if cfg.Theme.Primary != "#abcdef" {
		t.Errorf("Theme.Primary = %q, want #abcdef", cfg.Theme.Primary)
	}
}
