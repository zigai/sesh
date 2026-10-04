package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const (
	testSocketFlag          = "--socket"
	socketRequiredErrorText = "--socket must be an explicit absolute path"
)

func requireSocketUsageError(t *testing.T, err error) {
	t.Helper()

	if err == nil || clibase.ExitCode(err) != clibase.ExitCodeUsage || !strings.Contains(err.Error(), socketRequiredErrorText) {
		t.Fatalf("error = %v, want the explicit socket usage error", err)
	}
}

func TestDaemonTmuxRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	for _, socket := range []string{"relative.sock", "/tmp/../tmp/unclean.sock"} {
		exec := clitest.Execute(t, "daemon", "tmux", testSocketFlag, socket, "--no-config")
		requireSocketUsageError(t, exec.Err)
	}
}

func TestDaemonCoordinatorRequiresExplicitSocketBeforeOpeningLogind(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t,
		"daemon", "coordinator",
		testSocketFlag, "relative.sock",
		"--history", filepath.Join(t.TempDir(), "history.json"),
		"--no-config",
	)
	if exec.Err == nil || clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage || errors.Is(exec.Err, clibase.ErrUnknownCommand) {
		t.Fatalf("daemon coordinator error = %v, want socket usage error", exec.Err)
	}
}

func TestTmuxWaitReadyRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	for _, socket := range []string{"relative.sock", "/tmp/../tmp/unclean.sock"} {
		exec := clitest.Execute(t, "action", "tmux-wait-ready", testSocketFlag, socket, "--no-config")
		requireSocketUsageError(t, exec.Err)
	}
}

func TestTmuxWaitReadyTimesOutWithoutServer(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t,
		"action", "tmux-wait-ready",
		testSocketFlag, filepath.Join(t.TempDir(), "nonexistent.sock"),
		"--timeout", "50ms",
		"--no-config",
	)
	if !errors.Is(exec.Err, context.DeadlineExceeded) {
		t.Fatalf("tmux-wait-ready error = %v, want the readiness deadline", exec.Err)
	}
}
