package config_test

import (
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestTmuxKeyNameWritesKeysAsTmuxListsThem(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"F13":      "S-F1",
		"F24":      "S-F12",
		"M-F24":    "M-S-F12",
		"C-M-F15":  "C-M-S-F3",
		"M-F12":    "M-F12",
		"M-n":      "M-n",
		"S-M-F12":  "M-S-F12",
		"M-C-x":    "C-M-x",
		"s-m-c-y":  "C-M-S-y",
		"^b":       "C-b",
		"S-M-Left": "M-S-Left",
		"M--":      "M--",
		"-":        "-",
		"C-":       "C-",
	} {
		if got := config.TmuxKeyName(input); got != want {
			t.Errorf("TmuxKeyName(%q) = %q, want %q", input, got, want)
		}
	}

	if got := config.TmuxKeyName(" M-X "); got != "M-X" {
		t.Errorf("TmuxKeyName(%q) = %q, want surrounding space trimmed", " M-X ", got)
	}
}
