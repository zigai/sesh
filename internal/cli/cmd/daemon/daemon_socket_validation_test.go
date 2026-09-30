package daemon_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const testSocketFlag = "--socket"

func TestDaemonServeRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t, "daemon", "serve", testSocketFlag, "relative.sock", "--no-config")
	if exec.Err == nil || clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("daemon serve error = %v, want usage error", exec.Err)
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

func TestDaemonWaitReadyRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t, "daemon", "wait-ready", testSocketFlag, "relative.sock", "--no-config")
	if exec.Err == nil || clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("daemon wait-ready error = %v, want usage error", exec.Err)
	}
}

func TestDaemonWaitReadyTimesOutWithoutServer(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t,
		"daemon", "wait-ready",
		testSocketFlag, filepath.Join(t.TempDir(), "nonexistent.sock"),
		"--timeout", "50ms",
		"--no-config",
	)
	if exec.Err == nil {
		t.Fatal("wait-ready without answering server must fail")
	}
}
