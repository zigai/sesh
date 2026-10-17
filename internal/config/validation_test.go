package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
		for _, style := range []string{"none", "indent", "right"} {
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

func TestLoadValidatesSessionKeyAction(t *testing.T) {
	for action, valid := range map[string]bool{"focus": true, "open": true, "jump": false, "": false} {
		t.Run(action, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tempDir)

			configDir := filepath.Join(tempDir, "sesh")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("mkdir failed: %v", err)
			}

			content := "[settings]\nsession_key_action = \"" + action + "\"\n"
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
				t.Fatalf("write config failed: %v", err)
			}

			_, err := config.Load()
			if valid && err != nil {
				t.Fatalf("Load() rejected session_key_action %q: %v", action, err)
			}

			if !valid && (err == nil || !strings.Contains(err.Error(), "session_key_action")) {
				t.Fatalf("Load() error = %v, want session_key_action error for %q", err, action)
			}
		})
	}
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
		{name: "undefined custom field", content: "[display]\nsesh_format = \"{custom.weather}\"", want: "custom.weather has no display.fields.weather entry"},
		{name: "undefined custom fallback", content: "[display]\nagents_agent_format = \"{status|custom.ci}\"", want: "display.agents_agent_format"},
		{name: "custom field name", content: "[display.fields]\nBad-Name = \"date\"", want: "invalid display.fields.Bad-Name"},
		{name: "empty custom command", content: "[display.fields]\nx = \" \"", want: "invalid display.fields.x: command cannot be empty"},
		{name: "custom refresh", content: "[display.fields]\nx = { command = \"date\", refresh = \"0s\" }", want: "refresh must be a positive duration"},
		{name: "custom refresh syntax", content: "[display.fields]\nx = { command = \"date\", refresh = \"soon\" }", want: "refresh must be a positive duration"},
		{name: "custom unknown key", content: "[display.fields]\nx = { command = \"date\", timeout = \"1s\" }", want: "unknown field key: timeout"},
		{name: "custom args type", content: "[display.fields]\nx = { command = \"date\", args = [1] }", want: "field args must be strings"},
		{name: "custom value type", content: "[display.fields]\nx = 3", want: "field must be a shell command string or table"},
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

func sessionKeyConfig(prefix string, sessions map[string]string) config.KeyConfig {
	return config.KeyConfig{SessionPrefix: prefix, AgentKeys: "asdf", Sessions: sessions}
}

func TestValidateKeyConfigRejectsInvalidSessionKeys(t *testing.T) {
	tests := map[string]config.KeyConfig{
		"upper case letter":          sessionKeyConfig("tab", map[string]string{"web": "W"}),
		"digit":                      sessionKeyConfig("tab", map[string]string{"web": "1"}),
		"two letters":                sessionKeyConfig("tab", map[string]string{"web": "we"}),
		"empty session name":         sessionKeyConfig("tab", map[string]string{"": "w"}),
		"letter used twice":          sessionKeyConfig("tab", map[string]string{"web": "w", "wiki": "w"}),
		"letter used twice with alt": sessionKeyConfig("alt", map[string]string{"web": "w", "wiki": "w"}),
	}

	navigation := sessionKeyConfig("alt", map[string]string{"web": "w"})
	navigation.Top = "alt+w"
	tests["navigation key with alt"] = navigation

	for name, keys := range tests {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateKeyConfig(keys); err == nil {
				t.Fatalf("ValidateKeyConfig(%v) error = nil", keys.Sessions)
			}
		})
	}

	for _, prefix := range []string{"tab", "alt"} {
		if err := config.ValidateKeyConfig(sessionKeyConfig(prefix, map[string]string{"web": "w", "api": "a"})); err != nil {
			t.Fatalf("ValidateKeyConfig() error = %v for valid session keys with prefix %s", err, prefix)
		}
	}

	tabbed := sessionKeyConfig("tab", map[string]string{"web": "w"})
	tabbed.Top = "alt+w"

	if err := config.ValidateKeyConfig(tabbed); err != nil {
		t.Fatalf("ValidateKeyConfig() error = %v; alt+w is free when sessions use the tab prefix", err)
	}
}

func TestValidateKeyConfigChecksSessionPrefix(t *testing.T) {
	tests := map[string]string{
		"empty":                 "",
		"types into the search": "x",
		"space":                 "space",
		"closes the picker":     "esc",
		"selects a row":         "enter",
		"navigation key":        "home",
	}

	for name, prefix := range tests {
		t.Run(name, func(t *testing.T) {
			keys := sessionKeyConfig(prefix, nil)
			keys.Top = "home"

			if err := config.ValidateKeyConfig(keys); err == nil {
				t.Fatalf("ValidateKeyConfig() accepted session_prefix %q", prefix)
			}
		})
	}

	for _, prefix := range []string{"tab", "alt", "ctrl+g", "f2"} {
		if err := config.ValidateKeyConfig(sessionKeyConfig(prefix, nil)); err != nil {
			t.Fatalf("ValidateKeyConfig() rejected session_prefix %q: %v", prefix, err)
		}
	}
}

func TestValidateKeyConfigChecksAgentKeys(t *testing.T) {
	for _, agentKeys := range []string{"", "asa", "aS", "a;", "a b"} {
		keys := sessionKeyConfig("tab", nil)
		keys.AgentKeys = agentKeys

		if err := config.ValidateKeyConfig(keys); err == nil {
			t.Fatalf("ValidateKeyConfig() accepted agent_keys %q", agentKeys)
		}
	}

	for _, agentKeys := range []string{"1234567890", "fjdksla", "q"} {
		keys := sessionKeyConfig("tab", nil)
		keys.AgentKeys = agentKeys

		if err := config.ValidateKeyConfig(keys); err != nil {
			t.Fatalf("ValidateKeyConfig() rejected agent_keys %q: %v", agentKeys, err)
		}
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

func TestLoadReadsProjectsAndTasks(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := `[projects]
roots = ["~/code", "/srv/repos"]
extra = ["~/notes"]

[tasks]
popup_socket = "popup"
popup_config = "~/.config/tmux/popup.conf"

[[tasks.harness]]
name = "codex"
command = ["codex", "--yolo"]

[[tasks.harness]]
name = "claude"
command = ["claude"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !slices.Equal(cfg.Projects.Roots, []string{"~/code", "/srv/repos"}) || !slices.Equal(cfg.Projects.Extra, []string{"~/notes"}) {
		t.Errorf("Projects = %+v", cfg.Projects)
	}

	if cfg.Tasks.PopupSocket != "popup" || cfg.Tasks.PopupConfig != "~/.config/tmux/popup.conf" {
		t.Errorf("Tasks = %+v", cfg.Tasks)
	}

	if len(cfg.Tasks.Harness) != 2 || cfg.Tasks.Harness[0].Name != "codex" || !slices.Equal(cfg.Tasks.Harness[0].Command, []string{"codex", "--yolo"}) {
		t.Errorf("Tasks.Harness = %+v, want codex first with its arguments", cfg.Tasks.Harness)
	}
}

func TestLoadAcceptsSupportedHarnessesByNameAlone(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := "[[tasks.harness]]\nname = \"pi\"\n\n[[tasks.harness]]\nname = \"claude\"\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Tasks.Harness) != 2 || cfg.Tasks.Harness[0].Name != "pi" || len(cfg.Tasks.Harness[0].Command) != 0 {
		t.Fatalf("Tasks.Harness = %+v, want pi then claude without commands", cfg.Tasks.Harness)
	}
}

func TestLoadRejectsInvalidProjectsAndTasks(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "relative root", content: "[projects]\nroots = [\"Projects\"]"},
		{name: "relative extra folder", content: "[projects]\nextra = [\"notes\"]"},
		{name: "pane named agent", content: "[[tasks.panes]]\nname = \"agent\""},
		{name: "duplicate pane", content: "[[tasks.panes]]\nname = \"editor\"\n[[tasks.panes]]\nname = \"editor\""},
		{name: "pane unknown variable", content: "[[tasks.panes]]\nname = \"logs\"\ncommand = [\"tail\", \"{repo}\"]"},
		{name: "layout without arrangement", content: "[[tasks.layouts]]\nfocus = \"agent\""},
		{name: "layout with arrange and splits", content: "[[tasks.layouts]]\narrange = \"tiled\"\nsplits = [{ pane = \"editor\", of = \"agent\", direction = \"right\" }]"},
		{name: "unknown arrange", content: "[[tasks.layouts]]\narrange = \"spiral\""},
		{name: "main size without main arrange", content: "[[tasks.layouts]]\narrange = \"tiled\"\nmain_size = \"60%\""},
		{name: "main size of 100 percent", content: "[[tasks.layouts]]\narrange = \"main-vertical\"\nmain_size = \"100%\""},
		{name: "arrange of an unknown pane", content: "[[tasks.layouts]]\narrange = \"tiled\"\npanes = [\"shell\"]"},
		{name: "split of an unknown pane", content: "[[tasks.layouts]]\nsplits = [{ pane = \"shell\", of = \"agent\", direction = \"right\" }]"},
		{name: "split of a later pane", content: "[[tasks.panes]]\nname = \"editor\"\n[[tasks.panes]]\nname = \"shell\"\n[[tasks.layouts]]\nsplits = [{ pane = \"shell\", of = \"editor\", direction = \"below\" }, { pane = \"editor\", of = \"agent\", direction = \"right\" }]"},
		{name: "pane placed twice", content: "[[tasks.layouts]]\nsplits = [{ pane = \"editor\", of = \"agent\", direction = \"right\" }, { pane = \"editor\", of = \"agent\", direction = \"below\" }]"},
		{name: "split direction", content: "[[tasks.layouts]]\nsplits = [{ pane = \"editor\", of = \"agent\", direction = \"up\" }]"},
		{name: "split size", content: "[[tasks.layouts]]\nsplits = [{ pane = \"editor\", of = \"agent\", direction = \"right\", size = \"half\" }]"},
		{name: "focus on an unplaced pane", content: "[[tasks.layouts]]\narrange = \"tiled\"\npanes = []\nfocus = \"editor\""},
		{name: "orientation", content: "[[tasks.layouts]]\narrange = \"tiled\"\nwhen = { orientation = \"wide\" }"},
		{name: "max width below min width", content: "[[tasks.layouts]]\narrange = \"tiled\"\nwhen = { min_width = 200, max_width = 100 }"},
		{name: "duplicate layout name", content: "[[tasks.layouts]]\nname = \"a\"\narrange = \"tiled\"\n[[tasks.layouts]]\nname = \"a\"\narrange = \"tiled\""},
		{name: "pane without a program", content: "[[tasks.panes]]\nname = \"logs\"\ncommand = [\" \", \"-f\"]"},
		{name: "restart without a command", content: "[[tasks.panes]]\nname = \"shell\"\nrestart = true"},
		{name: "duplicate project pane", content: "[[projects.overrides.shop.task_panes]]\nname = \"editor\"\n[[projects.overrides.shop.task_panes]]\nname = \"editor\""},
		{name: "layout for a harness that is not offered", content: "[[tasks.harness]]\nname = \"pi\"\n[[tasks.layouts]]\narrange = \"tiled\"\nwhen = { harness = [\"claud\"] }"},
		{name: "relative override path", content: "[[projects.overrides.\"work/api\".task_panes]]\nname = \"server\""},
		{name: "project layout of a missing pane", content: "[[projects.overrides.shop.task_layouts]]\narrange = \"tiled\"\npanes = [\"server\"]"},
		{name: "socket path", content: "[tasks]\npopup_socket = \"/tmp/popup\""},
		{name: "relative popup config", content: "[tasks]\npopup_config = \"popup.conf\""},
		{name: "reserved harness name", content: "[[tasks.harness]]\nname = \"tab\"\ncommand = [\"bash\"]"},
		{name: "unknown harness without command", content: "[[tasks.harness]]\nname = \"myagent\"\ncommand = []"},
		{name: "harness with empty program", content: "[[tasks.harness]]\nname = \"claude\"\ncommand = [\" \"]"},
		{name: "harness name with spaces", content: "[[tasks.harness]]\nname = \"my agent\"\ncommand = [\"agent\"]"},
		{
			name:    "duplicate harness",
			content: "[[tasks.harness]]\nname = \"pi\"\ncommand = [\"pi\"]\n[[tasks.harness]]\nname = \"pi\"\ncommand = [\"pi\", \"--fast\"]",
		},
		{name: "wake on restore word", content: "[tasks]\nwake_on_restore = \"all\""},
		{name: "wake on restore zero", content: "[tasks]\nwake_on_restore = \"0\""},
		{name: "menu item without run", content: "[menu.items.ship]\nprompt = \"ship it\""},
		{name: "menu item unknown variable", content: "[menu.items.ship]\nrun = [\"claude\", \"-p\"]\nprompt = \"ship {repo}\""},
		{name: "menu item prompt and file", content: "[menu.items.ship]\nrun = [\"claude\"]\nprompt = \"a\"\nprompt_file = \"b.md\""},
		{name: "project override of a missing item", content: "[projects.overrides.shop.menu_items.ship]\nprompt = \"ship\""},
		{name: "menu entry that is no item", content: "[menu]\nagent = [\"go\", \"review\"]"},
		{name: "session menu with an agent entry", content: "[menu]\nsession = [\"go\", \"snooze\"]"},
		{name: "menu entry listed twice", content: "[menu]\nagent = [\"go\", \"go\"]"},
		{name: "inbox group", content: "[inbox]\ngroup = \"host\""},
		{name: "inbox unknown section", content: "[inbox]\nsections = [\"done\", \"later\"]"},
		{name: "inbox no sections", content: "[inbox]\nsections = []"},
		{name: "inbox label of unknown section", content: "[inbox.labels]\nlater = \"zzz\""},
		{name: "inbox snooze word", content: "[inbox]\nsnooze = [\"soon\"]"},
		{name: "inbox snooze negative", content: "[inbox]\nsnooze = [\"-1h\"]"},
		{name: "inbox snooze tomorrow without time", content: "[inbox]\nsnooze = [\"tomorrow 2h\"]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := config.Load(config.WithExplicitPath(configPath)); err == nil {
				t.Fatal("Load() error = nil, want rejection")
			}
		})
	}
}

func TestMenuItemsForAppliesProjectOverridesFieldByField(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := `[menu.items.ship]
key = "alt+p"
run = ["claude", "-p"]
prompt = "ship {branch}"
link_pr = true

[menu.items.review]
run = ["codex", "exec"]

[projects.overrides.shop.menu_items.ship]
prompt_file = ".agents/ship.md"
link_pr = false
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	shop := cfg.MenuItemsFor("/code/shop")["ship"]
	if shop.Key != "alt+p" || !slices.Equal(shop.Run, []string{"claude", "-p"}) || shop.Prompt != "" ||
		shop.PromptFile != ".agents/ship.md" || shop.LinksPR() {
		t.Errorf("shop ship = %+v, want the global action with the override's prompt file and no PR link", shop)
	}

	other := cfg.MenuItemsFor("/code/other")["ship"]
	if other.Prompt != "ship {branch}" || other.PromptFile != "" || !other.LinksPR() {
		t.Errorf("other ship = %+v, want the global action", other)
	}

	if _, found := cfg.MenuItemsFor("/code/shop")["review"]; !found {
		t.Error("shop lost the review action it does not override")
	}
}

func TestLoadReplacesTheDefaultTaskPanes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := `[[tasks.panes]]
name = "edit"
command = ["hx", "."]
restart = true

[[tasks.layouts]]
when = { orientation = "landscape", min_width = 200 }
splits = [{ pane = "edit", of = "agent", direction = "right", size = "40%" }]
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Tasks.Panes) != 1 || cfg.Tasks.Panes[0].Name != "edit" || !slices.Equal(cfg.Tasks.Panes[0].Command, []string{"hx", "."}) || !cfg.Tasks.Panes[0].Restart {
		t.Errorf("Tasks.Panes = %+v, want only the configured pane", cfg.Tasks.Panes)
	}

	if len(cfg.Tasks.Layouts) != 1 || cfg.Tasks.Layouts[0].When.MinWidth != 200 || cfg.Tasks.Layouts[0].Splits[0].Size != "40%" {
		t.Errorf("Tasks.Layouts = %+v", cfg.Tasks.Layouts)
	}
}

func TestProjectTaskWindowReplacesPanesByNameAndLayoutsWhole(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := `[[tasks.panes]]
name = "editor"
command = ["nvim"]

[[tasks.panes]]
name = "shell"

[[tasks.layouts]]
name = "wide"
arrange = "main-vertical"

[[projects.overrides.shop.task_panes]]
name = "editor"
command = ["hx"]

[[projects.overrides.shop.task_panes]]
name = "server"
command = ["npm", "run", "dev"]
restart = true

[[projects.overrides.shop.task_layouts]]
name = "grid"
arrange = "tiled"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	shop := cfg.TaskWindowFor("/code/shop")

	names := make([]string, 0, len(shop.Panes))
	for _, pane := range shop.Panes {
		names = append(names, pane.Name)
	}

	if !slices.Equal(names, []string{"editor", "shell", "server"}) || !slices.Equal(shop.Panes[0].Command, []string{"hx"}) {
		t.Errorf("shop panes = %+v, want editor replaced, shell kept, server added", shop.Panes)
	}

	if len(shop.Layouts) != 1 || shop.Layouts[0].Name != "grid" {
		t.Errorf("shop layouts = %+v, want only the project's", shop.Layouts)
	}

	other := cfg.TaskWindowFor("/code/other")
	if len(other.Panes) != 2 || len(other.Layouts) != 1 || other.Layouts[0].Name != "wide" {
		t.Errorf("other project window = %+v, want the global one", other)
	}
}

func TestTaskLayoutWhenMatches(t *testing.T) {
	tests := []struct {
		name          string
		when          config.TaskLayoutWhen
		width, height int
		harness       string
		want          bool
	}{
		{name: "anything", when: config.TaskLayoutWhen{}, width: 10, height: 10, harness: "pi", want: true},
		{name: "landscape at twice as wide", when: config.TaskLayoutWhen{Orientation: config.OrientationLandscape}, width: 200, height: 100, want: true},
		{name: "landscape too narrow", when: config.TaskLayoutWhen{Orientation: config.OrientationLandscape}, width: 199, height: 100, want: false},
		{name: "portrait", when: config.TaskLayoutWhen{Orientation: config.OrientationPortrait}, width: 120, height: 100, want: true},
		{name: "below min width", when: config.TaskLayoutWhen{MinWidth: 160}, width: 159, height: 40, want: false},
		{name: "above max height", when: config.TaskLayoutWhen{MaxHeight: 50}, width: 100, height: 51, want: false},
		{name: "other harness", when: config.TaskLayoutWhen{Harness: []string{"claude"}}, width: 100, height: 50, harness: "pi", want: false},
		{name: "listed harness", when: config.TaskLayoutWhen{Harness: []string{"claude", "pi"}}, width: 100, height: 50, harness: "pi", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.when.Matches(tt.width, tt.height, tt.harness); got != tt.want {
				t.Errorf("Matches(%d, %d, %q) = %v, want %v", tt.width, tt.height, tt.harness, got, tt.want)
			}
		})
	}
}

func TestAProjectPathOverrideWinsOverItsDirectoryName(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	work := t.TempDir()

	content := `[[projects.overrides.api.task_panes]]
name = "server"
command = ["make", "serve"]

[[projects.overrides."` + filepath.Join(work, "api") + `".task_panes]]
name = "server"
command = ["npm", "start"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	server := func(root string) []string {
		pane, _ := cfg.TaskWindowFor(root).Pane("server")

		return pane.Command
	}

	if got := server(filepath.Join(work, "api")); !slices.Equal(got, []string{"npm", "start"}) {
		t.Errorf("server pane of the api at the overridden path = %v, want npm start", got)
	}

	if got := server("/code/api"); !slices.Equal(got, []string{"make", "serve"}) {
		t.Errorf("server pane of another api = %v, want the name override's make serve", got)
	}
}

func TestAnEmptyProjectLayoutListUsesTheDefaultLayouts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	content := `[[tasks.layouts]]
name = "grid"
arrange = "tiled"

[projects.overrides.shop]
task_layouts = []
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.TaskWindowFor("/code/shop").Layouts; len(got) != len(config.DefaultTaskLayouts()) || got[0].Name == "grid" {
		t.Fatalf("shop layouts = %+v, want the default layouts", got)
	}
}
