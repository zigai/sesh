package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/zigai/sesh/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	assertEqual(t, "Primary theme", cfg.Theme.Primary, "#24c8ff")
	assertEqual(t, "Warning theme", cfg.Theme.Warning, "#ff9e3b")
	assertEqual(t, "Create key", cfg.Keys.Create, "alt+c")
	assertEqual(t, "Rename key", cfg.Keys.Rename, "alt+r")
	assertEqual(t, "Kill key", cfg.Keys.Kill, "alt+x")
	assertEqual(t, "Delete key", cfg.Keys.Delete, "alt+shift+x")
	assertEqual(t, "Help key", cfg.Keys.Help, "?")
	assertEqual(t, "CycleMode key", cfg.Keys.CycleMode, "ctrl+f")
	assertEqual(t, "SESH popup width", cfg.Popup.SeshWidth, "40%")
	assertEqual(t, "SESH popup height", cfg.Popup.SeshHeight, "45%")
	assertEqual(t, "AGENTS popup width", cfg.Popup.AgentsWidth, "40%")
	assertEqual(t, "AGENTS popup height", cfg.Popup.AgentsHeight, "80%")
	assertEqual(t, "SESH display format", cfg.Display.SeshFormat, "{source_prefix}{session_name}{right}{session_agent_context}")
	assertEqual(t, "AGENTS session format", cfg.Display.AgentsSessionFormat, "{source_prefix}{session_name}")
	assertEqual(t, "AGENTS agent format", cfg.Display.AgentsAgentFormat, "{tree}{status_glyph} {status_age:<7}   {agent_cwd_name:<23.23}  {agent_session}{right}{harness:<6.6}   {agent_git_label:<6.22} {agent_git_added:<5} {agent_git_deleted:<5} {agent_git_ahead:<2} {agent_git_behind:<2}")
	assertEqual(t, "PageUp key", cfg.Keys.PageUp, "pgup")
	assertEqual(t, "PageDown key", cfg.Keys.PageDown, "pgdown")
	assertEqual(t, "Top key", cfg.Keys.Top, "home")
	assertEqual(t, "Bottom key", cfg.Keys.Bottom, "end")
	assertEqual(t, "Select1 key", cfg.Keys.Select1, "ctrl+1")
	assertEqual(t, "Select0 key", cfg.Keys.Select0, "ctrl+0")
	assertEqual(t, "ConfirmKill setting", cfg.Settings.ConfirmKill, true)
	assertEqual(t, "ConfirmDelete setting", cfg.Settings.ConfirmDelete, true)
	assertEqual(t, "ShowFooter setting", cfg.Settings.ShowFooter, true)
	assertEqual(t, "DirectorySuggestions setting", cfg.Settings.DirectorySuggestions, 5)
	assertEqual(t, "DefaultSessionPathSuggestion setting", cfg.Settings.DefaultSessionPathSuggestion, "~/Projects/{session}")
	assertEqual(t, "Snapshot retention days", cfg.Snapshot.RetentionDays, 14)
	assertEqual(t, "Snapshot maximum count", cfg.Snapshot.MaxCount, 0)
	assertEqual(t, "Snapshot maximum bytes", cfg.Snapshot.MaxBytes, int64(0))

	if !slices.Equal(cfg.Snapshot.ProcessEnvironment, config.DefaultProcessEnvironment) {
		t.Fatalf("ProcessEnvironment = %#v, want %#v", cfg.Snapshot.ProcessEnvironment, config.DefaultProcessEnvironment)
	}

	if !slices.Equal(cfg.Settings.DisplayModes, []string{"sesh", "agents"}) {
		t.Fatalf("DisplayModes = %#v, want sesh and agents", cfg.Settings.DisplayModes)
	}

	assertEqual(t, "AgentSessions enabled", cfg.AgentSessions.Enabled, true)
	assertEqual(t, "AgentSessions popup counts", cfg.AgentSessions.PopupCounts, true)
	assertEqual(t, "AgentSessions refresh interval", cfg.AgentSessions.RefreshInterval, "250ms")
	assertEqual(t, "AgentSessions status style", cfg.AgentSessions.StatusStyle, "glyph")
	assertEqual(t, "AgentSessions status time format", cfg.AgentSessions.StatusTimeFormat, "relative")

	assertEqual(t, "Remote enabled", cfg.Remote.Enabled, false)
	assertEqual(t, "Remote SSH path", cfg.Remote.SSHPath, "ssh")
	assertEqual(t, "Remote tmux poll interval", cfg.Remote.TmuxPollInterval, "1s")
	assertEqual(t, "Remote connect timeout", cfg.Remote.ConnectTimeout, "5s")
	assertEqual(t, "Remote heartbeat interval", cfg.Remote.HeartbeatInterval, "10s")
	assertEqual(t, "Remote stale interval", cfg.Remote.StaleAfter, "30s")
	assertEqual(t, "Remote hide interval", cfg.Remote.HideAfter, "10m")
	assertEqual(t, "Remote maximum parallel connections", cfg.Remote.MaxParallelConnects, 4)
	assertEqual(t, "Remote reverse bridge", cfg.Remote.EnableReverseBridge, true)
	assertEqual(t, "Remote return to origin", cfg.Remote.ReturnToOrigin, true)
}

func TestConfigPrecedenceAcrossUserProjectEnvironmentAndExplicitPath(t *testing.T) {
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

	check("13s")
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

func TestTOMLOmittedLogFileKeepsDefault(t *testing.T) {
	cfg := config.DefaultConfig()

	d := toml.NewDecoder(strings.NewReader("[paths]\ndata_dir = \"relative/data\"\n"))
	d.DisallowUnknownFields()

	if err := d.Decode(cfg); err != nil {
		t.Fatalf("decoding config without paths.log_file: %v", err)
	}

	if cfg.Paths.LogFile != "" {
		t.Fatalf("Paths.LogFile = %q, want empty default", cfg.Paths.LogFile)
	}
}

func TestPathConfigTOMLRoundTrip(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Paths.DataDir = "relative/data"
	cfg.Paths.LogFile = "relative/logs/sesh.jsonl"

	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}

	var decoded config.Config
	if err := toml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshaling config: %v", err)
	}

	if decoded.Paths != cfg.Paths {
		t.Fatalf("round-tripped Paths = %#v, want %#v", decoded.Paths, cfg.Paths)
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
create = "ctrl+g"
help = "f1"
cycle_mode = "ctrl+e"
page_up = "ctrl+b"
page_down = "ctrl+f"
top = "g"
bottom = "G"
select_1 = "alt+1"
select_0 = "alt+0"`

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
	assertEqual(t, "Create key", cfg.Keys.Create, "ctrl+g")
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
	assertEqual(t, "Select1 key", cfg.Keys.Select1, "alt+1")
	assertEqual(t, "Select0 key", cfg.Keys.Select0, "alt+0")
	assertEqual(t, "ConfirmKill default setting", cfg.Settings.ConfirmKill, true)
	assertEqual(t, "ConfirmDelete default setting", cfg.Settings.ConfirmDelete, true)
	assertEqual(t, "ShowFooter default setting", cfg.Settings.ShowFooter, true)
	assertEqual(t, "DirectorySuggestions default setting", cfg.Settings.DirectorySuggestions, 5)
	assertEqual(t, "DefaultSessionPathSuggestion default setting", cfg.Settings.DefaultSessionPathSuggestion, "~/Projects/{session}")
}

func TestLoadSettingsOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[settings]
confirm_kill = false
confirm_delete = false
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

	if cfg.Settings.ConfirmKill || cfg.Settings.ConfirmDelete || cfg.Settings.ShowFooter {
		t.Fatalf("settings = %#v, want confirmations disabled", cfg.Settings)
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
