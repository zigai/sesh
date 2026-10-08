package config_test

import (
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestPickerKeyNameWritesKeysAsTheTerminalReportsThem(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"alt+ctrl+x":   "ctrl+alt+x",
		"Shift+Ctrl+x": "ctrl+shift+x",
		"Alt+PgUp":     "alt+pgup",
		"PGUP":         "pgup",
		"alt+shift+x":  "alt+X",
		"shift+alt+x":  "alt+X",
		"alt+X":        "alt+X",
		"alt+x":        "alt+x",
		"ctrl+X":       "ctrl+x",
		"ctrl++":       "ctrl++",
		"+":            "+",
		"X":            "X",
	} {
		if got := config.PickerKeyName(input); got != want {
			t.Errorf("PickerKeyName(%q) = %q, want %q", input, got, want)
		}
	}

	if got := config.PickerKeyName(" ? "); got != "?" {
		t.Errorf("PickerKeyName(%q) = %q, want surrounding space trimmed", " ? ", got)
	}
}
