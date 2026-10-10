package picker_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

func TestPickRejectsNonTerminalBeforeRepositoryCreation(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")

	exec := clitest.ExecuteWithInput(t, strings.NewReader("not a terminal"),
		"pick", "--ui", "fullscreen", "--no-config", "--socket", "/tmp/sesh-picker-missing.sock",
		"--repository", repository,
	)
	if exec.Err == nil || !strings.Contains(exec.Err.Error(), "requires an interactive terminal") {
		t.Fatalf("pick error = %v, want terminal required", exec.Err)
	}

	if _, statErr := os.Stat(repository); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("repository created before terminal validation: %v", statErr)
	}
}

func TestPickRejectsUnknownUIAsUsageBeforeRepositoryCreation(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")

	exec := clitest.ExecuteWithInput(t, strings.NewReader(""),
		"pick", "--ui", "standalone", "--no-config", "--socket", "/tmp/sesh-picker-missing.sock",
		"--repository", repository,
	)
	if exec.Err == nil || !strings.Contains(exec.Err.Error(), "invalid picker ui") || clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("pick error = %v (exit %d), want invalid ui usage error", exec.Err, clibase.ExitCode(exec.Err))
	}

	if _, statErr := os.Stat(repository); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("repository created before ui validation: %v", statErr)
	}
}
