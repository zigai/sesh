package config

import (
	"errors"
	"strings"
)

var errThemePresetInvalid = errors.New("invalid theme.preset")

type ThemePreset struct {
	Name        string
	Primary     string
	Secondary   string
	Muted       string
	Error       string
	Warning     string
	Success     string
	Border      string
	Selected    string
	Background  string
	Foreground  string
	AgentStatus AgentStatusThemeConfig
}

var themePresets = map[string]ThemePreset{
	"dracula": {
		Name:       "dracula",
		Primary:    "#24c8ff",
		Secondary:  "#ffdd57",
		Muted:      "#6c7086",
		Error:      "#ff5555",
		Warning:    "#ff9e3b",
		Success:    "#50fa7b",
		Border:     "#3a3d45",
		Selected:   "#2a2a2a",
		Background: "#000000",
		Foreground: "#f8f8f2",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#50fa7b",
			Waiting:     "#ffdd57",
			Failed:      "#ff5555",
			Interrupted: "#ff9e3b",
			Idle:        "#6c7086",
		},
	},
	"catppuccin": {
		Name:       "catppuccin",
		Primary:    "#89b4fa",
		Secondary:  "#f9e2af",
		Muted:      "#6c7086",
		Error:      "#f38ba8",
		Warning:    "#fab387",
		Success:    "#a6e3a1",
		Border:     "#45475a",
		Selected:   "#313244",
		Background: "#1e1e2e",
		Foreground: "#cdd6f4",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#a6e3a1",
			Waiting:     "#f9e2af",
			Failed:      "#f38ba8",
			Interrupted: "#fab387",
			Idle:        "#6c7086",
		},
	},
	"catppuccin-mocha": {
		Name:       "catppuccin-mocha",
		Primary:    "#89b4fa",
		Secondary:  "#f9e2af",
		Muted:      "#6c7086",
		Error:      "#f38ba8",
		Warning:    "#fab387",
		Success:    "#a6e3a1",
		Border:     "#45475a",
		Selected:   "#313244",
		Background: "#1e1e2e",
		Foreground: "#cdd6f4",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#a6e3a1",
			Waiting:     "#f9e2af",
			Failed:      "#f38ba8",
			Interrupted: "#fab387",
			Idle:        "#6c7086",
		},
	},
	"catppuccin-latte": {
		Name:       "catppuccin-latte",
		Primary:    "#1e66f5",
		Secondary:  "#df8e1d",
		Muted:      "#9ca0b0",
		Error:      "#d20f39",
		Warning:    "#fe640b",
		Success:    "#40a02b",
		Border:     "#bcc0cc",
		Selected:   "#ccd0da",
		Background: "#eff1f5",
		Foreground: "#4c4f69",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#40a02b",
			Waiting:     "#df8e1d",
			Failed:      "#d20f39",
			Interrupted: "#fe640b",
			Idle:        "#9ca0b0",
		},
	},
	"tokyo-night": {
		Name:       "tokyo-night",
		Primary:    "#7aa2f7",
		Secondary:  "#e0af68",
		Muted:      "#565f89",
		Error:      "#f7768e",
		Warning:    "#ff9e64",
		Success:    "#9ece6a",
		Border:     "#3b4261",
		Selected:   "#283457",
		Background: "#1a1b26",
		Foreground: "#c0caf5",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#9ece6a",
			Waiting:     "#e0af68",
			Failed:      "#f7768e",
			Interrupted: "#ff9e64",
			Idle:        "#565f89",
		},
	},
	"gruvbox": {
		Name:       "gruvbox",
		Primary:    "#83a598",
		Secondary:  "#fabd2f",
		Muted:      "#928374",
		Error:      "#fb4934",
		Warning:    "#fe8019",
		Success:    "#b8bb26",
		Border:     "#504945",
		Selected:   "#3c3836",
		Background: "#282828",
		Foreground: "#ebdbb2",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#b8bb26",
			Waiting:     "#fabd2f",
			Failed:      "#fb4934",
			Interrupted: "#fe8019",
			Idle:        "#928374",
		},
	},
	"nord": {
		Name:       "nord",
		Primary:    "#88c0d0",
		Secondary:  "#ebcb8b",
		Muted:      "#4c566a",
		Error:      "#bf616a",
		Warning:    "#d08770",
		Success:    "#a3be8c",
		Border:     "#434c5e",
		Selected:   "#3b4252",
		Background: "#2e3440",
		Foreground: "#eceff4",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "#a3be8c",
			Waiting:     "#ebcb8b",
			Failed:      "#bf616a",
			Interrupted: "#d08770",
			Idle:        "#4c566a",
		},
	},
	"terminal": {
		Name:       "terminal",
		Primary:    "6",
		Secondary:  "3",
		Muted:      "8",
		Error:      "1",
		Warning:    "3",
		Success:    "2",
		Border:     "8",
		Selected:   "8",
		Background: "default",
		Foreground: "default",
		AgentStatus: AgentStatusThemeConfig{
			Running:     "2",
			Waiting:     "3",
			Failed:      "1",
			Interrupted: "3",
			Idle:        "8",
		},
	},
}

func LookupPreset(name string) (ThemePreset, bool) {
	norm := strings.ToLower(strings.TrimSpace(name))
	if norm == "" {
		return themePresets["dracula"], true
	}

	preset, ok := themePresets[norm]
	return preset, ok
}

func DefaultThemeConfig() ThemeConfig {
	p := themePresets["dracula"]
	return ThemeConfig{
		Preset:      p.Name,
		Primary:     p.Primary,
		Secondary:   p.Secondary,
		Muted:       p.Muted,
		Error:       p.Error,
		Warning:     p.Warning,
		Success:     p.Success,
		Border:      p.Border,
		Selected:    p.Selected,
		Background:  "",
		Foreground:  "",
		AgentStatus: nil,
	}
}

func (t *ThemeConfig) applyPreset(p ThemePreset) {
	t.Preset = p.Name
	t.Primary = p.Primary
	t.Secondary = p.Secondary
	t.Muted = p.Muted
	t.Error = p.Error
	t.Warning = p.Warning
	t.Success = p.Success
	t.Border = p.Border
	t.Selected = p.Selected
	t.Background = p.Background
	t.Foreground = p.Foreground
	cloned := p.AgentStatus
	t.AgentStatus = &cloned
}
