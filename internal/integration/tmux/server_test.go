package tmux

import (
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestOpenAdmitsFullTerminalTextOutput(t *testing.T) {
	server, err := Open(filepath.Join(t.TempDir(), "socket"))
	if err != nil {
		t.Fatal(err)
	}

	limits, defaults := server.Limits(), gotmux.DefaultLimits()
	if limits.OutputBytes != OutputBytes {
		t.Fatalf("output limit = %d, want %d", limits.OutputBytes, OutputBytes)
	}

	if limits.CommandTimeout != defaults.CommandTimeout || limits.InputBytes != defaults.InputBytes || limits.Concurrent != defaults.Concurrent {
		t.Fatalf("unrelated limits changed: %#v", limits)
	}
}
