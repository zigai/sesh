//go:build linux

package autosave_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/testsupport/clitest"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestStartupRestoreRefusalNamesTheBlockingRestoreAndItsFix(t *testing.T) {
	isolatedAutosaveHome(t)

	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	snapfixture.CommitOneSession(t, repoPath)

	self, err := process.ReadIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatalf("read own identity: %v", err)
	}

	live := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), self.String())

	exec := clitest.Execute(t, "autosave", "restore-on-start",
		testSocketFlag, filepath.Join(root, "absent.sock"), "--repository", repoPath,
		"--history", filepath.Join(root, "state", "history.json"), testNoConfigFlag)
	if exec.Err == nil {
		t.Fatal("a restore that may still run must refuse the startup restore")
	}

	if !strings.Contains(exec.Stderr, "startup restore refused") ||
		!strings.Contains(exec.Stderr, "sesh snapshot recover restore abandon "+live) {
		t.Fatalf("stderr must name the restore and how to abandon it:\n%s", exec.Stderr)
	}
}
