//go:build integration

package setup_test

import (
	"errors"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestSetupTmuxApplyTargetsAmbientServerWithoutSocket(t *testing.T) {
	t.Setenv("TMUX", filepath.Join(t.TempDir(), "ambient-tmux.sock")+",123,0")

	_, _, err := executeCommand(t, "setup", "tmux", "--apply", testNoConfigFlag)
	if !errors.Is(err, gotmux.ErrNoServer) {
		t.Fatalf("error = %v, want the stopped ambient server to report no tmux server", err)
	}
}
