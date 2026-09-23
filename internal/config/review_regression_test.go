package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

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

func TestLoadIntoDoesNotShareOrMutateCallerDefaults(t *testing.T) {
	t.Parallel()

	defaults := config.DefaultConfig()
	defaults.Settings.DisplayModes = []string{"sesh"}
	defaults.Remote.Connections = []config.RemoteConnectionConfig{{ID: "orig_conn", Host: "example.com"}}
	defaults.AgentStatus.Palette = map[string]string{"state": "#abcdef"}

	target := config.DefaultConfig()
	if err := config.LoadInto(target, config.WithDefaults(defaults), config.WithoutConfig()); err != nil {
		t.Fatalf("LoadInto() error = %v", err)
	}

	target.Settings.DisplayModes[0] = "agents"
	target.Remote.Connections[0].ID = "mutated_conn"
	target.AgentStatus.Palette["state"] = "mutated_color"

	if defaults.Settings.DisplayModes[0] != "sesh" {
		t.Errorf("defaults DisplayModes mutated: %q", defaults.Settings.DisplayModes[0])
	}

	if defaults.Remote.Connections[0].ID != "orig_conn" {
		t.Errorf("defaults Connections mutated: %q", defaults.Remote.Connections[0].ID)
	}

	if defaults.AgentStatus.Palette["state"] != "#abcdef" {
		t.Errorf("defaults Palette mutated: %q", defaults.AgentStatus.Palette["state"])
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

func TestSaveRejectsSymlinkTarget(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realFile := filepath.Join(tempDir, "real_target.toml")
	if err := os.WriteFile(realFile, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkFile := filepath.Join(tempDir, "symlink_target.toml")
	if err := os.Symlink(realFile, symlinkFile); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	cfg := config.DefaultConfig()

	_, err := config.Save(cfg, config.WithSavePath(symlinkFile))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Save() error = %v, want %v", err, config.ErrConfigSymlink)
	}

	data, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "original" {
		t.Fatalf("realFile was overwritten through symlink: %q", string(data))
	}
}

func TestSaveRejectsParentDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realDir := filepath.Join(tempDir, "real_dir")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}

	symlinkDir := filepath.Join(tempDir, "symlink_dir")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	cfg := config.DefaultConfig()

	_, err := config.Save(cfg, config.WithSavePath(filepath.Join(symlinkDir, "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Save() error = %v, want %v", err, config.ErrConfigSymlink)
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

func TestInitRejectsAncestorDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realGrandparent := filepath.Join(tempDir, "real_gp")
	if err := os.MkdirAll(realGrandparent, 0o700); err != nil {
		t.Fatal(err)
	}

	symlinkGrandparent := filepath.Join(tempDir, "symlink_gp")
	if err := os.Symlink(realGrandparent, symlinkGrandparent); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	_, err := config.Init(config.WithInitPath(filepath.Join(symlinkGrandparent, "nested", "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestSaveRejectsAncestorDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realGrandparent := filepath.Join(tempDir, "real_gp")
	if err := os.MkdirAll(realGrandparent, 0o700); err != nil {
		t.Fatal(err)
	}

	symlinkGrandparent := filepath.Join(tempDir, "symlink_gp")
	if err := os.Symlink(realGrandparent, symlinkGrandparent); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	cfg := config.DefaultConfig()

	_, err := config.Save(cfg, config.WithSavePath(filepath.Join(symlinkGrandparent, "nested", "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Save() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestDeterministicLeafReplacementSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	targetFile := filepath.Join(tempDir, "protected_target.txt")
	if err := os.WriteFile(targetFile, []byte("important-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(targetFile, configPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	cfg := config.DefaultConfig()

	_, err := config.Save(cfg, config.WithSavePath(configPath))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Save() error = %v, want %v", err, config.ErrConfigSymlink)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "important-data" {
		t.Fatalf("targetFile was overwritten: %q, want important-data", string(data))
	}
}

func TestDeterministicParentReplacementSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realParent := filepath.Join(tempDir, "real_parent")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatal(err)
	}

	parentDir := filepath.Join(tempDir, "parent_dir")
	if err := os.Symlink(realParent, parentDir); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	configPath := filepath.Join(parentDir, "config.toml")

	cfg := config.DefaultConfig()

	_, err := config.Save(cfg, config.WithSavePath(configPath))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Save() error = %v, want %v", err, config.ErrConfigSymlink)
	}

	_, err = config.Init(config.WithInitPath(configPath))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestInitNoClobberDoesNotOverwriteExistingFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	initialContent := []byte("# existing config\n[theme]\nprimary = \"#999999\"\n")
	if err := os.WriteFile(configPath, initialContent, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Init(config.WithInitPath(configPath), config.WithInitForce(false))
	if !errors.Is(err, config.ErrConfigExists) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigExists)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(initialContent) {
		t.Fatalf("existing file was modified: got %q, want %q", string(data), string(initialContent))
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

func TestResolvedAgentStatusPaletteDetached(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.AgentStatus.Palette = map[string]string{
		"waiting": "fg=blue",
	}

	resolved := cfg.ResolvedAgentStatus()
	resolved.Palette["waiting"] = "fg=red"
	resolved.Palette["new_key"] = "fg=green"

	if cfg.AgentStatus.Palette["waiting"] != "fg=blue" {
		t.Errorf("original palette mutated: %q, want fg=blue", cfg.AgentStatus.Palette["waiting"])
	}

	if _, exists := cfg.AgentStatus.Palette["new_key"]; exists {
		t.Error("new key leaked into original palette")
	}
}

func TestSparseAgentSessionsStatusPreservesBooleanDefaults(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	tomlContent := "[agent_sessions.status]\nstyle_waiting = \"fg=yellow\"\n"
	if err := os.WriteFile(configPath, []byte(tomlContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	resolved := cfg.ResolvedAgentStatus()
	if !resolved.Enabled {
		t.Error("resolved.Enabled unexpectedly reset to false")
	}

	if !resolved.ActiveBypass {
		t.Error("resolved.ActiveBypass unexpectedly reset to false")
	}

	if !resolved.ClearOnShutdown {
		t.Error("resolved.ClearOnShutdown unexpectedly reset to false")
	}

	if !resolved.SetWindowStatusStyle {
		t.Error("resolved.SetWindowStatusStyle unexpectedly reset to false")
	}

	if !resolved.SetWindowStatusBellStyle {
		t.Error("resolved.SetWindowStatusBellStyle unexpectedly reset to false")
	}

	if !resolved.ClearFailedOnVisit {
		t.Error("resolved.ClearFailedOnVisit unexpectedly reset to false")
	}

	if resolved.StyleWaiting != "fg=yellow" {
		t.Errorf("resolved.StyleWaiting = %q, want fg=yellow", resolved.StyleWaiting)
	}
}

func TestSparseAgentSessionsStatusPreservesExplicitFalse(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	tomlContent := "[agent_sessions.status]\nenabled = false\nactive_bypass = false\nstyle_waiting = \"fg=purple\"\n"
	if err := os.WriteFile(configPath, []byte(tomlContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	resolved := cfg.ResolvedAgentStatus()
	if resolved.Enabled {
		t.Error("resolved.Enabled should be false when explicitly set to false")
	}

	if resolved.ActiveBypass {
		t.Error("resolved.ActiveBypass should be false when explicitly set to false")
	}

	if !resolved.ClearOnShutdown {
		t.Error("resolved.ClearOnShutdown should remain default true when omitted")
	}

	if resolved.StyleWaiting != "fg=purple" {
		t.Errorf("resolved.StyleWaiting = %q, want fg=purple", resolved.StyleWaiting)
	}
}

func TestConcurrentInitWinnerIsFullValidConfig(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "concurrent_init.toml")

	const goroutines = 16

	var wg sync.WaitGroup

	errs := make([]error, goroutines)
	paths := make([]string, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			p, err := config.Init(config.WithInitPath(configPath), config.WithInitForce(false))
			errs[idx] = err
			paths[idx] = p
		}(i)
	}

	wg.Wait()

	winnerCount := 0

	for _, err := range errs {
		if err == nil {
			winnerCount++
		} else if !errors.Is(err, config.ErrConfigExists) {
			t.Fatalf("unexpected error from concurrent Init: %v", err)
		}
	}

	if winnerCount != 1 {
		t.Fatalf("winnerCount = %d, want exactly 1", winnerCount)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Fatal("config file on disk is empty")
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load(concurrent_init) failed: %v", err)
	}

	if cfg.Theme.Primary != "#24c8ff" {
		t.Errorf("Theme.Primary = %q, want #24c8ff", cfg.Theme.Primary)
	}
}
