package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
)

func TestLoadRejectsInvalidHookTimeout(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[hooks]
session_switch_post = [{ command = "echo", timeout = "soon" }]`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want hook timeout error")
	}

	if !strings.Contains(err.Error(), "invalid hooks.session_switch_post[0]") {
		t.Fatalf("Load() error = %v, want hook timeout context", err)
	}
}

func TestHookTimeoutsMustBePositive(t *testing.T) {
	tests := []struct {
		name  string
		hooks config.HookConfig
	}{
		{name: "global zero", hooks: config.HookConfig{Timeout: "0s"}},
		{name: "global negative", hooks: config.HookConfig{Timeout: "-1s"}},
		{name: "command zero", hooks: config.HookConfig{SessionSwitchPost: []config.HookCommand{{Command: "echo", Timeout: "0s"}}}},
		{name: "command negative", hooks: config.HookConfig{SessionSwitchPost: []config.HookCommand{{Command: "echo", Timeout: "-1s"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := config.ValidateHookConfig(tt.hooks); err == nil {
				t.Fatal("ValidateHookConfig() error = nil, want nonpositive timeout rejection")
			}
		})
	}
}

func TestLoadRejectsNonStringHookFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "command", content: `[hooks]
session_switch_post = [{ command = 42 }]`, want: "hook command must be a string"},
		{name: "timeout", content: `[hooks]
session_switch_post = [{ command = "echo", timeout = 42 }]`, want: "hook timeout must be a string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)

			configDir := filepath.Join(configHome, "sesh")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("creating config directory: %v", err)
			}

			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAgentSessionsDuration(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[agent_sessions]
refresh_interval = "soon"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want agent_sessions duration error")
	}

	if !strings.Contains(err.Error(), "invalid agent_sessions.refresh_interval") {
		t.Fatalf("Load() error = %v, want agent_sessions duration context", err)
	}
}

func TestAgentSessionsRefreshIntervalUsesResponsiveDefault(t *testing.T) {
	t.Parallel()

	const want = 250 * time.Millisecond
	for _, cfg := range []*config.Config{nil, config.DefaultConfig()} {
		if got := config.AgentSessionsRefreshInterval(cfg); got != want {
			t.Fatalf("AgentSessionsRefreshInterval() = %s, want %s", got, want)
		}
	}
}

func TestLoadRejectsDuplicateKeyBindings(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[keys]
create = "alt+r"
rename = "alt+r"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate key error")
	}

	if !strings.Contains(err.Error(), "duplicate key binding") ||
		!strings.Contains(err.Error(), "alt+r") {
		t.Fatalf("Load() error = %v, want duplicate binding context", err)
	}
}

func TestLoadRejectsDuplicateAgentNavigationBindings(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[agent_sessions]
next_idle_key = "M-]"
next_idle_local_key = "M-]"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "duplicate agent_sessions key binding") {
		t.Fatalf("Load() error = %v, want duplicate agent navigation binding error", err)
	}
}

func TestLoadRejectsCycleModeKeyCollision(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[keys]
page_down = "ctrl+f"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "cycle_mode") || !strings.Contains(err.Error(), "page_down") {
		t.Fatalf("Load() error = %v, want cycle mode collision context", err)
	}
}

func TestLoadRejectsInvalidDisplayModes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "[settings]\ndisplay_modes = []", want: "display modes cannot be empty"},
		{name: "unsupported", content: "[settings]\ndisplay_modes = [\"focus\"]", want: "invalid display mode"},
		{name: "duplicate", content: "[settings]\ndisplay_modes = [\"agents\", \"AGENTS\"]", want: "duplicate display mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tempDir)

			configDir := filepath.Join(tempDir, "sesh")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("mkdir failed: %v", err)
			}

			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write config failed: %v", err)
			}

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadValidatesShortcutStyle(t *testing.T) {
	t.Run("valid styles", func(t *testing.T) {
		for _, style := range []string{"none", "indent", "right", "numbers", "menu", "badge", "tree", "off"} {
			checkValidShortcutStyle(t, style)
		}
	})

	t.Run("invalid style", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tempDir)

		configDir := filepath.Join(tempDir, "sesh")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		content := "[settings]\nshortcut_style = \"floating\"\n"
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatalf("write config failed: %v", err)
		}

		_, err := config.Load()
		if err == nil || !strings.Contains(err.Error(), "invalid settings.shortcut_style") {
			t.Fatalf("Load() error = %v, want invalid settings.shortcut_style", err)
		}
	})
}

func checkValidShortcutStyle(t *testing.T, style string) {
	t.Helper()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	content := fmt.Sprintf("[settings]\nshortcut_style = %q\n", style)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() rejected valid shortcut_style %q: %v", style, err)
	}

	if cfg.Settings.NormalizedShortcutStyle() == "" {
		t.Fatalf("NormalizedShortcutStyle() returned empty for %q", style)
	}
}

func TestLoadRejectsInvalidDisplayFormats(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "[display]\nsesh_format = \"\"", want: "display.sesh_format"},
		{name: "unknown field", content: "[display]\nsesh_format = \"{session_path}\"", want: "unknown field"},
		{name: "agent field on parent", content: "[display]\nagents_session_format = \"{status}\"", want: "display.agents_session_format"},
		{name: "unclosed field", content: "[display]\nagents_agent_format = \"{status\"", want: "unclosed field"},
		{name: "multiple right markers", content: "[display]\nsesh_format = \"x{right}y{right}z\"", want: "at most one {right}"},
		{name: "invalid field alignment", content: "[display]\nsesh_format = \"{session_name:>wide}\"", want: "invalid field format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tempDir)

			configDir := filepath.Join(tempDir, "sesh")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("mkdir failed: %v", err)
			}

			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write config failed: %v", err)
			}

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidPopupDimensions(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "percentage range", value: "101%"},
		{name: "format expression", value: "#{client_width}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tempDir)

			configDir := filepath.Join(tempDir, "sesh")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("mkdir failed: %v", err)
			}

			content := "[popup]\nagents_height = \"" + tt.value + "\""
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
				t.Fatalf("write config failed: %v", err)
			}

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), "popup.agents_height") {
				t.Fatalf("Load() error = %v, want popup dimension context", err)
			}
		})
	}
}

func TestValidatePopupConfigAcceptsCellsAndPercentages(t *testing.T) {
	popup := config.PopupConfig{
		SeshWidth:    "80",
		SeshHeight:   "24",
		AgentsWidth:  "50%",
		AgentsHeight: "70%",
	}

	if err := config.ValidatePopupConfig(popup); err != nil {
		t.Fatalf("ValidatePopupConfig() failed: %v", err)
	}
}

func TestValidateRemoteConfigAcceptsExplicitConnection(t *testing.T) {
	remote := config.DefaultConfig().Remote
	remote.Enabled = true
	remote.MaxParallelConnects = 128
	remote.Connections = []config.RemoteConnectionConfig{{
		ID:         "work.us-east",
		Label:      "US East Work",
		Host:       "work.example.com",
		SeshPath:   "/usr/local/bin/sesh",
		TmuxSocket: "work",
	}}

	if err := config.ValidateRemoteConfig(remote); err != nil {
		t.Fatalf("ValidateRemoteConfig() error = %v", err)
	}
}

func TestValidateRemoteConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.RemoteConfig)
		wantErr string
	}{
		{
			name: "missing connection ID",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{Host: "host"}}
			},
			wantErr: "id",
		},
		{
			name: "connection ID starts with dash",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "-work", Host: "host"}}
			},
			wantErr: "cannot start with a dash",
		},
		{
			name: "connection ID contains control character",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work\n", Host: "host"}}
			},
			wantErr: "control characters",
		},
		{
			name: "invalid connection ID",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "team/name", Host: "host"}}
			},
			wantErr: "may contain only",
		},
		{
			name: "duplicate connection ID",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{
					{ID: "work", Host: "one"},
					{ID: "work", Host: "two"},
				}
			},
			wantErr: "duplicate connection ID",
		},
		{
			name: "missing host",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work"}}
			},
			wantErr: "host: value is required",
		},
		{
			name: "host starts with dash",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "-proxy"}}
			},
			wantErr: "cannot start with a dash",
		},
		{
			name: "host contains control character",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host\ncommand"}}
			},
			wantErr: "control characters",
		},
		{
			name: "SSH path starts with dash",
			mutate: func(remote *config.RemoteConfig) {
				remote.SSHPath = "-ssh"
			},
			wantErr: "ssh_path",
		},
		{
			name: "label contains control character",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host", Label: "Work\r"}}
			},
			wantErr: "control characters",
		},
		{
			name: "sesh path starts with dash",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host", SeshPath: "-sesh"}}
			},
			wantErr: "sesh_path",
		},
		{
			name: "sesh path contains control character",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host", SeshPath: "sesh\n"}}
			},
			wantErr: "control characters",
		},
		{
			name: "tmux socket starts with dash",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host", TmuxSocket: "-work"}}
			},
			wantErr: "tmux_socket",
		},
		{
			name: "tmux socket contains control character",
			mutate: func(remote *config.RemoteConfig) {
				remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Host: "host", TmuxSocket: "work\n"}}
			},
			wantErr: "control characters",
		},
		{
			name: "invalid poll interval",
			mutate: func(remote *config.RemoteConfig) {
				remote.TmuxPollInterval = "never"
			},
			wantErr: "tmux_poll_interval",
		},
		{
			name: "nonpositive heartbeat interval",
			mutate: func(remote *config.RemoteConfig) {
				remote.HeartbeatInterval = "0s"
			},
			wantErr: "heartbeat_interval",
		},
		{
			name: "stale threshold before heartbeat",
			mutate: func(remote *config.RemoteConfig) {
				remote.StaleAfter = remote.HeartbeatInterval
			},
			wantErr: "stale_after must be greater",
		},
		{
			name: "hide threshold before stale",
			mutate: func(remote *config.RemoteConfig) {
				remote.HideAfter = remote.StaleAfter
			},
			wantErr: "hide_after must be greater",
		},
		{
			name: "nonpositive concurrency",
			mutate: func(remote *config.RemoteConfig) {
				remote.MaxParallelConnects = 0
			},
			wantErr: "max_parallel_connects",
		},
		{
			name: "concurrency exceeds connection limit",
			mutate: func(remote *config.RemoteConfig) {
				remote.MaxParallelConnects = 129
			},
			wantErr: "max_parallel_connects",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := config.DefaultConfig().Remote
			tt.mutate(&remote)

			err := config.ValidateRemoteConfig(remote)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateRemoteConfig() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRejectsRemoteConcurrencyAboveConnectionLimit(t *testing.T) {
	tests := []struct {
		name string
		load func(t *testing.T) (*config.Config, error)
	}{
		{
			name: "TOML",
			load: func(t *testing.T) (*config.Config, error) {
				t.Helper()

				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte("[remote]\nmax_parallel_connects = 129\n"), 0o600); err != nil {
					t.Fatal(err)
				}

				return config.Load(config.WithExplicitPath(path))
			},
		},
		{
			name: "environment",
			load: func(t *testing.T) (*config.Config, error) {
				t.Helper()
				t.Setenv("SESH_REMOTE_MAX_PARALLEL_CONNECTS", "129")

				return config.Load(config.WithoutConfig())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.load(t)
			if err == nil || !strings.Contains(err.Error(), "remote.max_parallel_connects") {
				t.Fatalf("Load() error = %v, want bounded remote concurrency error", err)
			}
		})
	}
}

func TestDefaultRemotePermissionsDisableDestructiveActions(t *testing.T) {
	permissions := config.DefaultConfig().Remote.Permissions
	if !permissions.Create || !permissions.Rename || !permissions.Snapshot || !permissions.Restore {
		t.Fatalf("non-destructive remote permissions = %#v, want enabled", permissions)
	}

	if permissions.Kill || permissions.Delete {
		t.Fatalf("destructive remote permissions = %#v, want disabled", permissions)
	}
}

func TestValidateAgentSessionsStatusStyle(t *testing.T) {
	tests := []struct {
		name    string
		style   string
		wantErr string
	}{
		{name: "empty valid", style: ""},
		{name: "glyph valid", style: "glyph"},
		{name: "text valid", style: "text"},
		{name: "circle valid", style: "circle"},
		{name: "circles valid", style: "circles"},
		{name: "icon valid", style: "icon"},
		{name: "icons valid", style: "icons"},
		{name: "uppercase valid", style: "GLYPH"},
		{name: "invalid style", style: "emoji", wantErr: "invalid agent_sessions.status_style"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig().AgentSessions
			cfg.StatusStyle = tt.style

			err := config.ValidateAgentSessionsConfig(cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateAgentSessionsConfig() unexpected error = %v", err)
			}

			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateAgentSessionsConfig() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAgentSessionsStatusTimeFormat(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		isStyle bool
		wantErr string
	}{
		{name: "empty valid", format: ""},
		{name: "relative valid", format: "relative"},
		{name: "relative seconds valid", format: "relative-seconds"},
		{name: "compact valid", format: "compact"},
		{name: "human valid", format: "human"},
		{name: "clock valid", format: "clock"},
		{name: "duration valid", format: "duration"},
		{name: "hms valid", format: "hms"},
		{name: "hh:mm:ss valid", format: "hh:mm:ss"},
		{name: "hh:mm valid", format: "hh:mm"},
		{name: "hh:mm uppercase valid", format: " HH:MM "},
		{name: "00:00:00 valid", format: "00:00:00"},
		{name: "00:01:23 valid", format: "00:01:23"},
		{name: "timer valid", format: "timer"},
		{name: "uppercase valid", format: "CLOCK"},
		{name: "whitespace trimmed", format: "  00:01:23  "},
		{name: "invalid format", format: "seconds", wantErr: "invalid agent_sessions.status_time_format"},
		{name: "style empty valid", isStyle: true, format: ""},
		{name: "style clock valid", isStyle: true, format: "clock"},
		{name: "style 00:01:23 valid", isStyle: true, format: "00:01:23"},
		{name: "invalid style", isStyle: true, format: "seconds", wantErr: "invalid agent_sessions.status_time_style"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig().AgentSessions
			if tt.isStyle {
				cfg.StatusTimeFormat = ""
				cfg.StatusTimeStyle = tt.format
			} else {
				cfg.StatusTimeFormat = tt.format
			}

			err := config.ValidateAgentSessionsConfig(cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateAgentSessionsConfig() unexpected error = %v", err)
			}

			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateAgentSessionsConfig() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLogLevelValidation(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		wantErr string
	}{
		{name: "empty defaults to info", level: ""},
		{name: "debug valid", level: "debug"},
		{name: "info valid", level: "info"},
		{name: "warn valid", level: "warn"},
		{name: "error valid", level: "error"},
		{name: "disabled valid", level: "disabled"},
		{name: "uppercase valid", level: "DEBUG"},
		{name: "whitespace trimmed", level: "  info  "},
		{name: "invalid level", level: "verbose", wantErr: "invalid settings.log_level"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)

			configDir := filepath.Join(configHome, "sesh")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("creating config directory: %v", err)
			}

			content := fmt.Sprintf("[settings]\nlog_level = %q\n", tt.level)
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			_, err := config.Load()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Load() with log_level %q unexpected error = %v", tt.level, err)
			}

			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Load() with log_level %q error = %v, want %q", tt.level, err, tt.wantErr)
			}
		})
	}
}

func TestLogLevelConfigIntegration(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	configDir := filepath.Join(tempDir, "sesh")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	configContent := `[settings]
log_level = "invalid_level"`

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want log_level validation error")
	}

	if !strings.Contains(err.Error(), "invalid settings.log_level") {
		t.Fatalf("Load() error = %v, want invalid settings.log_level", err)
	}
}

func TestValidateKeyConfigCaseInsensitiveDuplicates(t *testing.T) {
	keys := config.KeyConfig{
		PageUp:   "PGUP",
		PageDown: "pgup",
	}

	err := config.ValidateKeyConfig(keys)
	if err == nil || !strings.Contains(err.Error(), "duplicate key binding") {
		t.Fatalf("ValidateKeyConfig() error = %v, want duplicate key binding error", err)
	}
}

func TestValidateAutosaveConfig(t *testing.T) {
	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Interval: "-1m"}); err == nil {
		t.Fatal("ValidateAutosaveConfig() error = nil for negative interval")
	}

	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Enabled: true, Interval: "0s"}); err == nil {
		t.Fatal("ValidateAutosaveConfig() error = nil for enabled zero interval")
	}

	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Enabled: true, Interval: ""}); err == nil {
		t.Fatal("ValidateAutosaveConfig() error = nil for enabled empty interval")
	}

	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Enabled: false, Interval: ""}); err != nil {
		t.Fatalf("ValidateAutosaveConfig() error = %v for disabled empty interval", err)
	}

	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Enabled: true, Interval: "3m"}); err != nil {
		t.Fatalf("ValidateAutosaveConfig() error = %v for valid 3m interval", err)
	}

	if err := config.ValidateAutosaveConfig(config.AutosaveConfig{Enabled: true, Interval: "180s"}); err != nil {
		t.Fatalf("ValidateAutosaveConfig() error = %v for valid 180s interval", err)
	}
}

func TestValidateAgentSessionsRefreshIntervalPositive(t *testing.T) {
	for _, invalid := range []string{"0s", "-100ms", "-1s"} {
		err := config.ValidateAgentSessionsConfig(config.AgentSessionsConfig{RefreshInterval: invalid})
		if err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Fatalf("ValidateAgentSessionsConfig() error = %v, want positive duration error for %q", err, invalid)
		}
	}
}

func TestValidateAgentStatusConfig(t *testing.T) {
	t.Run("valid defaults", func(t *testing.T) {
		cfg := config.DefaultConfig().AgentStatus
		if err := config.ValidateAgentStatusConfig(cfg); err != nil {
			t.Fatalf("ValidateAgentStatusConfig() error = %v, want nil", err)
		}
	})

	t.Run("invalid initial backoff duration", func(t *testing.T) {
		cfg := config.AgentStatusConfig{InitialBackoff: "invalid"}
		if err := config.ValidateAgentStatusConfig(cfg); err == nil {
			t.Fatal("ValidateAgentStatusConfig() error = nil for invalid duration")
		}
	})

	t.Run("non-positive initial backoff", func(t *testing.T) {
		for _, dur := range []string{"0s", "-1s"} {
			cfg := config.AgentStatusConfig{InitialBackoff: dur}
			if err := config.ValidateAgentStatusConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be positive") {
				t.Fatalf("ValidateAgentStatusConfig() error = %v, want positive error for %s", err, dur)
			}
		}
	})

	t.Run("invalid max backoff duration", func(t *testing.T) {
		cfg := config.AgentStatusConfig{MaxBackoff: "invalid"}
		if err := config.ValidateAgentStatusConfig(cfg); err == nil {
			t.Fatal("ValidateAgentStatusConfig() error = nil for invalid max backoff")
		}
	})

	t.Run("max backoff less than initial backoff", func(t *testing.T) {
		cfg := config.AgentStatusConfig{InitialBackoff: "5s", MaxBackoff: "1s"}
		if err := config.ValidateAgentStatusConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be greater than or equal") {
			t.Fatalf("ValidateAgentStatusConfig() error = %v, want max >= initial error", err)
		}
	})

	t.Run("negative backoff factor", func(t *testing.T) {
		cfg := config.AgentStatusConfig{BackoffFactor: -1.0}
		if err := config.ValidateAgentStatusConfig(cfg); err == nil || !strings.Contains(err.Error(), "must be non-negative") {
			t.Fatalf("ValidateAgentStatusConfig() error = %v, want non-negative factor error", err)
		}
	})
}

func TestAgentStatusPaletteResolution(t *testing.T) {
	status := config.AgentStatusConfig{
		StyleWaiting: "fg=yellow",
		Palette: map[string]string{
			"failed": "fg=red,bold",
		},
	}

	palette := status.ResolvedPalette()
	if palette["waiting"] != "fg=yellow" {
		t.Errorf("waiting style = %q, want fg=yellow", palette["waiting"])
	}

	if palette["failed"] != "fg=red,bold" {
		t.Errorf("failed style = %q, want fg=red,bold", palette["failed"])
	}

	if palette["unread"] != "fg=#1e1e2e,bg=#6c7086,bold" {
		t.Errorf("unread style = %q, want default", palette["unread"])
	}
}

func TestResolvedAgentStatusMerging(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentSessions.Status = &config.AgentStatusConfig{
		Enabled:      false,
		StyleWaiting: "fg=black,bg=yellow",
	}

	resolved := cfg.ResolvedAgentStatus()
	if resolved.Enabled {
		t.Error("expected resolved agent status to be disabled from nested override")
	}

	if resolved.StyleWaiting != "fg=black,bg=yellow" {
		t.Errorf("expected resolved StyleWaiting to be fg=black,bg=yellow, got %q", resolved.StyleWaiting)
	}

	if resolved.MaxBackoff != "5s" {
		t.Errorf("expected MaxBackoff to be 5s, got %q", resolved.MaxBackoff)
	}
}

func TestValidateSnapshotProcessEnvironmentValid(t *testing.T) {
	cfg := config.DefaultConfig()
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig(default) = %v", err)
	}

	cfg.Snapshot.ProcessEnvironment = []string{}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig(empty env) = %v", err)
	}

	cfg.Snapshot.ProcessEnvironment = []string{"PATH", "VIRTUAL_ENV", "CONDA_PREFIX"}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig(custom env) = %v", err)
	}
}

func TestValidateSnapshotProcessEnvironmentInvalidNames(t *testing.T) {
	t.Run("rejects empty variable name", func(t *testing.T) {
		err := config.ValidateProcessEnvironmentKeys([]string{""})
		if !errors.Is(err, config.ErrSnapshotProcessEnvInvalid) {
			t.Fatalf("ValidateProcessEnvironmentKeys(empty) = %v, want ErrSnapshotProcessEnvInvalid", err)
		}
	})

	t.Run("rejects variable name containing equals", func(t *testing.T) {
		err := config.ValidateProcessEnvironmentKeys([]string{"FOO=bar"})
		if !errors.Is(err, config.ErrSnapshotProcessEnvInvalid) {
			t.Fatalf("ValidateProcessEnvironmentKeys(equals) = %v, want ErrSnapshotProcessEnvInvalid", err)
		}
	})

	t.Run("rejects variable name containing null", func(t *testing.T) {
		err := config.ValidateProcessEnvironmentKeys([]string{"FOO\x00BAR"})
		if !errors.Is(err, config.ErrSnapshotProcessEnvInvalid) {
			t.Fatalf("ValidateProcessEnvironmentKeys(null) = %v, want ErrSnapshotProcessEnvInvalid", err)
		}
	})
}

func TestValidateSnapshotProcessEnvironmentLimits(t *testing.T) {
	t.Run("rejects too many keys", func(t *testing.T) {
		keys := make([]string, config.MaxProcessEnvironmentKeys+1)
		for i := range keys {
			keys[i] = fmt.Sprintf("KEY_%d", i)
		}

		err := config.ValidateProcessEnvironmentKeys(keys)
		if !errors.Is(err, config.ErrSnapshotProcessEnvLimit) {
			t.Fatalf("ValidateProcessEnvironmentKeys(excessive count) = %v, want ErrSnapshotProcessEnvLimit", err)
		}
	})

	t.Run("rejects key exceeding max length", func(t *testing.T) {
		longKey := strings.Repeat("A", config.MaxProcessEnvironmentKeyLength+1)

		err := config.ValidateProcessEnvironmentKeys([]string{longKey})
		if !errors.Is(err, config.ErrSnapshotProcessEnvLimit) {
			t.Fatalf("ValidateProcessEnvironmentKeys(excessive key length) = %v, want ErrSnapshotProcessEnvLimit", err)
		}
	})

	t.Run("rejects total bytes exceeding limit", func(t *testing.T) {
		keys := make([]string, 17)
		for i := range keys {
			keys[i] = fmt.Sprintf("K%03d_%s", i, strings.Repeat("X", 245))
		}

		err := config.ValidateProcessEnvironmentKeys(keys)
		if !errors.Is(err, config.ErrSnapshotProcessEnvLimit) {
			t.Fatalf("ValidateProcessEnvironmentKeys(excessive total bytes) = %v, want ErrSnapshotProcessEnvLimit", err)
		}
	})
}

func TestFilterProcessEnvironmentKeys(t *testing.T) {
	input := []string{"PATH", "TMUX", "VIRTUAL_ENV", "TMUX_PANE", "PATH", "NODE_PATH"}
	filtered := config.FilterProcessEnvironmentKeys(input)
	expected := []string{"PATH", "VIRTUAL_ENV", "NODE_PATH"}

	if !slices.Equal(filtered, expected) {
		t.Fatalf("FilterProcessEnvironmentKeys = %#v, want %#v", filtered, expected)
	}

	if config.FilterProcessEnvironmentKeys(nil) != nil {
		t.Fatal("FilterProcessEnvironmentKeys(nil) != nil")
	}

	if config.FilterProcessEnvironmentKeys([]string{}) != nil {
		t.Fatal("FilterProcessEnvironmentKeys([]) != nil")
	}

	if config.FilterProcessEnvironmentKeys([]string{"TMUX", "TMUX_PANE"}) != nil {
		t.Fatal("FilterProcessEnvironmentKeys(all excluded) != nil")
	}
}
