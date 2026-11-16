//go:build integration

package agent_test

import (
	"errors"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestAgentNavigationWithoutSocketTargetsDefaultTmuxServer(t *testing.T) {
	for _, verb := range []string{"next", "latest", "back"} {
		t.Run(verb, func(t *testing.T) {
			t.Setenv("TMUX", "")

			_, _, err := executeCommand(t, "agent", verb, "--no-config")
			if !errors.Is(err, gotmux.ErrNoServer) {
				t.Fatalf(
					"error = %v, want the isolated default socket to report no tmux server",
					err,
				)
			}

			if code := clibase.ExitCode(err); code != clibase.ExitCodeGeneral {
				t.Fatalf("exit code = %d, want %d", code, clibase.ExitCodeGeneral)
			}
		})
	}
}
