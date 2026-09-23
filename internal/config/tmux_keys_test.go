package config_test

import (
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestTmuxKeyNameConvertsExtendedFunctionKeys(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"F13":     "S-F1",
		"F24":     "S-F12",
		"M-F24":   "M-S-F12",
		"C-M-F15": "C-M-S-F3",
		"M-F12":   "M-F12",
		"M-n":     "M-n",
	} {
		if got := config.TmuxKeyName(input); got != want {
			t.Errorf("TmuxKeyName(%q) = %q, want %q", input, got, want)
		}
	}
}
