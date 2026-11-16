//go:build integration

package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/testsupport/clitest"
)

func TestTmuxWaitReadyTimesOutWithoutServer(t *testing.T) {
	t.Parallel()

	exec := clitest.Execute(t,
		"daemon", "wait-ready",
		testSocketFlag, filepath.Join(t.TempDir(), "nonexistent.sock"),
		"--timeout", "50ms",
		"--no-config",
	)
	if !errors.Is(exec.Err, context.DeadlineExceeded) {
		t.Fatalf("wait-ready error = %v, want the readiness deadline", exec.Err)
	}
}
