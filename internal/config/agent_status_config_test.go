package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

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
