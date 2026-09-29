package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

func TestStrataRejectsUnknownTopLevelKey(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	err := os.WriteFile(configPath, []byte("unknown_top_level = 42\nsecret_value = \"super-secret-token\"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(config.WithExplicitPath(configPath))
	if err == nil {
		t.Fatal("Load() error = nil, want unknown top level field rejection")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected error to be enriched with oops: %v", err)
	}

	if oopsErr.Domain() != "config" {
		t.Errorf("Domain = %q, want config", oopsErr.Domain())
	}

	if oopsErr.Code() != "config_invalid" {
		t.Errorf("Code = %q, want config_invalid", oopsErr.Code())
	}

	ctx := oopsErr.Context()
	if ctx["operation"] != "load" {
		t.Errorf("operation = %v, want load", ctx["operation"])
	}

	if ctx["reason"] != "unknown_field" {
		t.Errorf("reason = %v, want unknown_field", ctx["reason"])
	}

	if _, exists := ctx["field"]; exists {
		t.Errorf("field unexpectedly leaked into oops context: %v", ctx["field"])
	}

	strictErr, ok := errors.AsType[*toml.StrictMissingError](err)
	if !ok || len(strictErr.Errors) == 0 || strings.Join(strictErr.Errors[0].Key(), ".") != "unknown_top_level" {
		t.Errorf("typed parse cause does not preserve unknown_top_level: %v", err)
	}

	assertNoValuesLeakedInContext(t, ctx, "super-secret-token", "42")
}

func assertNoValuesLeakedInContext(t *testing.T, ctx map[string]any, forbidden ...string) {
	t.Helper()

	for k, v := range ctx {
		s := strings.ToLower(fmt.Sprintf("%s=%v", k, v))
		for _, f := range forbidden {
			if strings.Contains(s, strings.ToLower(f)) {
				t.Errorf("rejected value leaked in oops context %s: %s", k, v)
			}
		}
	}
}

func TestStrataRejectsUnknownNestedKey(t *testing.T) {
	assertRejectedConfigField(t, "[theme]\nbad_theme_field = \"#ffffff\"\n", "theme.bad_theme_field", "unknown_field", "#ffffff")
}

func TestStrataRejectsUnknownKeyInHooks(t *testing.T) {
	t.Run("table unknown field", func(t *testing.T) {
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.toml")

		err := os.WriteFile(configPath, []byte("[hooks]\nunknown_hook_setting = \"val\"\n"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		_, err = config.Load(config.WithExplicitPath(configPath))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("Load() error = %v, want unknown field error", err)
		}
	})

	t.Run("command object unknown field", func(t *testing.T) {
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.toml")

		err := os.WriteFile(configPath, []byte("[hooks]\nsession_switch_post = [{ command = \"echo\", bad_key = 123 }]\n"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		_, err = config.Load(config.WithExplicitPath(configPath))
		if err == nil || !strings.Contains(err.Error(), "unknown hook field") {
			t.Fatalf("Load() error = %v, want unknown hook field error", err)
		}
	})
}

func TestStrataRejectsDuplicateKey(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	err := os.WriteFile(configPath, []byte("[settings]\nconfirm_kill = true\nconfirm_kill = false\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(config.WithExplicitPath(configPath))
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate key rejection")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected error to be enriched with oops: %v", err)
	}

	ctx := oopsErr.Context()
	if ctx["reason"] != "duplicate_key" {
		t.Errorf("reason = %v, want duplicate_key", ctx["reason"])
	}

	if _, exists := ctx["field"]; exists {
		t.Errorf("field unexpectedly leaked into oops context: %v", ctx["field"])
	}

	decodeErr, ok := errors.AsType[*toml.DecodeError](err)
	if !ok || strings.Join(decodeErr.Key(), ".") != "confirm_kill" {
		t.Errorf("typed parse cause does not preserve duplicate field confirm_kill: %v", err)
	}
}

func TestStrataRejectsInvalidType(t *testing.T) {
	assertRejectedConfigField(t, "[settings]\ndirectory_suggestions = \"not-a-number\"\n", "settings.directory_suggestions", "invalid_type", "not-a-number")
}

func assertRejectedConfigField(t *testing.T, tomlContent, wantField, wantReason, forbiddenValue string) {
	t.Helper()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	err := os.WriteFile(configPath, []byte(tomlContent), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(config.WithExplicitPath(configPath))
	if err == nil {
		t.Fatalf("Load() error = nil, want %s rejection for field %s", wantReason, wantField)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected error to be enriched with oops: %v", err)
	}

	ctx := oopsErr.Context()
	if _, exists := ctx["field"]; exists {
		t.Errorf("field unexpectedly leaked into oops context: %v", ctx["field"])
	}

	var detectedField string
	if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok && len(strictErr.Errors) > 0 {
		detectedField = strings.Join(strictErr.Errors[0].Key(), ".")
	} else if decodeErr, ok := errors.AsType[*toml.DecodeError](err); ok {
		detectedField = strings.Join(decodeErr.Key(), ".")
	}

	if detectedField != wantField {
		t.Errorf("typed error field = %q, want %q", detectedField, wantField)
	}

	if ctx["reason"] != wantReason {
		t.Errorf("reason = %v, want %s", ctx["reason"], wantReason)
	}

	for k, v := range ctx {
		if strings.Contains(fmt.Sprint(v), forbiddenValue) {
			t.Errorf("rejected value leaked in oops context %s: %s", k, v)
		}
	}
}

func TestStrataPreservesUnsetVsExplicitFalseAndZeroAndEmpty(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	tomlData := `
[settings]
confirm_kill = false
directory_suggestions = 0
display_modes = ["agents"]
`
	if err := os.WriteFile(configPath, []byte(tomlData), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Settings.ConfirmKill {
		t.Errorf("ConfirmKill = true, want explicit false")
	}

	if !cfg.Settings.ConfirmDelete {
		t.Errorf("ConfirmDelete = false, want default true preserved")
	}

	if !cfg.Settings.ShowFooter {
		t.Errorf("ShowFooter = false, want default true preserved")
	}

	if cfg.Settings.DirectorySuggestions != 0 {
		t.Errorf("DirectorySuggestions = %d, want explicit 0", cfg.Settings.DirectorySuggestions)
	}

	if !slices.Equal(cfg.Settings.DisplayModes, []string{"agents"}) {
		t.Errorf("DisplayModes = %v, want explicit [agents]", cfg.Settings.DisplayModes)
	}

	if cfg.Theme.Primary != "#24c8ff" {
		t.Errorf("Theme.Primary = %q, want default #24c8ff", cfg.Theme.Primary)
	}

	if cfg.Keys.Create != "alt+c" {
		t.Errorf("Keys.Create = %q, want default alt+c", cfg.Keys.Create)
	}
}

func TestStrataSparseNestedOverlays(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	tomlData := `
[popup]
sesh_width = "40%"

[theme]
primary = "#112233"
`
	if err := os.WriteFile(configPath, []byte(tomlData), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Popup.SeshWidth != "40%" {
		t.Errorf("Popup.SeshWidth = %q, want 40%%", cfg.Popup.SeshWidth)
	}

	if cfg.Popup.SeshHeight != "45%" {
		t.Errorf("Popup.SeshHeight = %q, want default 45%%", cfg.Popup.SeshHeight)
	}

	if cfg.Popup.AgentsWidth != "40%" {
		t.Errorf("Popup.AgentsWidth = %q, want default 40%%", cfg.Popup.AgentsWidth)
	}

	if cfg.Popup.AgentsHeight != "80%" {
		t.Errorf("Popup.AgentsHeight = %q, want default 80%%", cfg.Popup.AgentsHeight)
	}

	if cfg.Theme.Primary != "#112233" {
		t.Errorf("Theme.Primary = %q, want #112233", cfg.Theme.Primary)
	}

	if cfg.Theme.Secondary != "#ffdd57" {
		t.Errorf("Theme.Secondary = %q, want default #ffdd57", cfg.Theme.Secondary)
	}
}

func TestStrataHooksStructuredArgvAndShellStrings(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	tomlData := `
[hooks]
timeout = "15s"
session_switch_pre = [
  "echo pre-shell",
  { command = "git", args = ["fetch", "--all"], timeout = "5s" },
]
session_switch_post = [
  { command = "tmux", args = ["display-message", "switched"] }
]
`
	if err := os.WriteFile(configPath, []byte(tomlData), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Hooks.Timeout != "15s" {
		t.Errorf("Hooks.Timeout = %q, want 15s", cfg.Hooks.Timeout)
	}

	verifyPreHooks(t, cfg.Hooks.SessionSwitchPre)
	verifyPostHooks(t, cfg.Hooks.SessionSwitchPost)
}

func verifyPreHooks(t *testing.T, pre []config.HookCommand) {
	t.Helper()

	if len(pre) != 2 {
		t.Fatalf("SessionSwitchPre len = %d, want 2", len(pre))
	}

	pre0 := pre[0]
	if pre0.ShellCommand != "echo pre-shell" || pre0.Command != "" {
		t.Errorf("pre0 = %#v, want shell command 'echo pre-shell'", pre0)
	}

	pre1 := pre[1]
	if pre1.ShellCommand != "" || pre1.Command != "git" || !slices.Equal(pre1.Args, []string{"fetch", "--all"}) || pre1.Timeout != "5s" {
		t.Errorf("pre1 = %#v, want structured command 'git fetch --all'", pre1)
	}
}

func verifyPostHooks(t *testing.T, post []config.HookCommand) {
	t.Helper()

	if len(post) != 1 {
		t.Fatalf("SessionSwitchPost len = %d, want 1", len(post))
	}

	post0 := post[0]
	if post0.Command != "tmux" || !slices.Equal(post0.Args, []string{"display-message", "switched"}) {
		t.Errorf("post0 = %#v, want structured command 'tmux display-message switched'", post0)
	}
}

func TestLoadPrecedenceNoConfigVsExplicitPath(t *testing.T) {
	tempDir := t.TempDir()

	corruptPath := filepath.Join(tempDir, "corrupt.toml")
	if err := os.WriteFile(corruptPath, []byte("invalid TOML syntax [[["), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(config.WithExplicitPath(corruptPath))
	if err == nil {
		t.Fatal("Load(explicit corrupt) error = nil, want syntax error")
	}

	cfg, err := config.Load(config.WithExplicitPath(corruptPath), config.WithoutConfig())
	if err != nil {
		t.Fatalf("Load(WithoutConfig) error = %v, want defaults", err)
	}

	if cfg == nil || cfg.Theme.Primary != "#24c8ff" {
		t.Fatalf("Load(WithoutConfig) did not return defaults: %#v", cfg)
	}
}

func TestLoadRejectsExplicitMissingFile(t *testing.T) {
	tempDir := t.TempDir()
	missingPath := filepath.Join(tempDir, "missing.toml")

	_, err := config.Load(config.WithExplicitPath(missingPath))
	if err == nil {
		t.Fatal("Load(missingPath) error = nil, want error for missing explicit path")
	}

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missingPath) error = %v, want os.ErrNotExist", err)
	}
}
