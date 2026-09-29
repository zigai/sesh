package config

import (
	"errors"
	"strings"
)

var errThemePresetInvalid = errors.New("invalid theme.preset")

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
		Background: "",
		Foreground: "",
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
	},
}

type ThemePreset struct {
	Name       string
	Primary    string
	Secondary  string
	Muted      string
	Error      string
	Warning    string
	Success    string
	Border     string
	Selected   string
	Background string
	Foreground string
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
	theme := ThemeConfig{
		Preset:      "",
		Primary:     "",
		Secondary:   "",
		Muted:       "",
		Error:       "",
		Warning:     "",
		Success:     "",
		Border:      "",
		Selected:    "",
		Background:  "",
		Foreground:  "",
		AgentStatus: nil,
	}
	theme.applyPreset(themePresets["dracula"])

	return theme
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
}
