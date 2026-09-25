package config_test

import (
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestLookupPresets(t *testing.T) {
	presets := []string{
		"dracula",
		"catppuccin",
		"catppuccin-mocha",
		"catppuccin-latte",
		"tokyo-night",
		"gruvbox",
		"nord",
		"terminal",
	}

	for _, name := range presets {
		p, ok := config.LookupPreset(name)
		if !ok {
			t.Fatalf("LookupPreset(%q) = false, want true", name)
		}
		if p.Primary == "" || p.Error == "" || p.Success == "" {
			t.Fatalf("preset %q missing essential colors: %+v", name, p)
		}
	}

	// Empty defaults to dracula
	p, ok := config.LookupPreset("")
	if !ok || p.Name != "dracula" {
		t.Fatalf("LookupPreset(\"\") = (%+v, %v), want dracula", p, ok)
	}

	// Unknown preset
	_, ok = config.LookupPreset("nonexistent")
	if ok {
		t.Fatal("LookupPreset(\"nonexistent\") = true, want false")
	}
}

func TestLoadThemePreset(t *testing.T) {
	tomlData := `
[theme]
preset = "tokyo-night"
`
	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(tomlData)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Theme.Preset != "tokyo-night" {
		t.Errorf("Theme.Preset = %q, want tokyo-night", cfg.Theme.Preset)
	}
	if cfg.Theme.Primary != "#7aa2f7" {
		t.Errorf("Theme.Primary = %q, want #7aa2f7", cfg.Theme.Primary)
	}
	if cfg.Theme.Error != "#f7768e" {
		t.Errorf("Theme.Error = %q, want #f7768e", cfg.Theme.Error)
	}
	if cfg.Theme.Secondary != "#e0af68" {
		t.Errorf("Theme.Secondary = %q, want #e0af68", cfg.Theme.Secondary)
	}
}

func TestLoadThemePresetWithFieldOverrides(t *testing.T) {
	tomlData := `
[theme]
preset = "catppuccin"
primary = "#ffffff"
`
	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(tomlData)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Theme.Preset != "catppuccin" {
		t.Errorf("Theme.Preset = %q, want catppuccin", cfg.Theme.Preset)
	}
	if cfg.Theme.Primary != "#ffffff" {
		t.Errorf("Theme.Primary = %q, want #ffffff (explicit override)", cfg.Theme.Primary)
	}
	if cfg.Theme.Secondary != "#f9e2af" {
		t.Errorf("Theme.Secondary = %q, want #f9e2af (preset default)", cfg.Theme.Secondary)
	}
}

func TestLoadThemePresetRejectsInvalid(t *testing.T) {
	tomlData := `
[theme]
preset = "invalid-preset"
`
	_, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(tomlData)))
	if err == nil {
		t.Fatal("Load() error = nil, want invalid theme.preset error")
	}
	if !strings.Contains(err.Error(), "invalid theme.preset") {
		t.Fatalf("Load() error = %v, want invalid theme.preset", err)
	}
}

func TestResolvedAgentStatusColorsAndPalette(t *testing.T) {
	t.Run("default colors from theme", func(t *testing.T) {
		cfg := config.DefaultConfig()
		colors := cfg.ResolvedAgentStatusColors()

		if colors.Running != cfg.Theme.Success {
			t.Errorf("Running = %q, want %q", colors.Running, cfg.Theme.Success)
		}
		if colors.Waiting != cfg.Theme.Secondary {
			t.Errorf("Waiting = %q, want %q", colors.Waiting, cfg.Theme.Secondary)
		}
		if colors.Failed != cfg.Theme.Error {
			t.Errorf("Failed = %q, want %q", colors.Failed, cfg.Theme.Error)
		}
	})

	t.Run("theme agent_status override", func(t *testing.T) {
		tomlData := `
[theme]
error = "#ff0000"

[theme.agent_status]
failed = "#aa0000"
`
		cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(tomlData)))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		colors := cfg.ResolvedAgentStatusColors()
		if colors.Failed != "#aa0000" {
			t.Errorf("Failed = %q, want #aa0000", colors.Failed)
		}

		palette := cfg.ResolvedPalette()
		if !strings.Contains(palette["failed"], "#aa0000") {
			t.Errorf("palette[failed] = %q, want containing #aa0000", palette["failed"])
		}
	})

	t.Run("agent_status colors override", func(t *testing.T) {
		tomlData := `
[agent_status.colors]
failed = "#cc0000"
`
		cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(tomlData)))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		colors := cfg.ResolvedAgentStatusColors()
		if colors.Failed != "#cc0000" {
			t.Errorf("Failed = %q, want #cc0000", colors.Failed)
		}

		palette := cfg.ResolvedPalette()
		if !strings.Contains(palette["failed"], "#cc0000") {
			t.Errorf("palette[failed] = %q, want containing #cc0000", palette["failed"])
		}
	})
}

func TestSynthesizeTmuxStyle(t *testing.T) {
	tests := []struct {
		color string
		want  string
	}{
		{color: "#ffdd57", want: "fg=#1e1e2e,bg=#ffdd57,bold"},
		{color: "#ff5555", want: "fg=#1e1e2e,bg=#ff5555,bold"},
		{color: "#000000", want: "fg=#ffffff,bg=#000000,bold"},
		{color: "red", want: "fg=black,bg=red,bold"},
		{color: "default", want: "bold"},
		{color: "", want: "bold"},
	}

	for _, tt := range tests {
		got := config.SynthesizeTmuxStyle(tt.color)
		if got != tt.want {
			t.Errorf("SynthesizeTmuxStyle(%q) = %q, want %q", tt.color, got, tt.want)
		}
	}
}
