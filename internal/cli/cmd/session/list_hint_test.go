package session_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func isolatedDataHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("TMUX", "")

	return home
}

func TestEmptySessionListPointsAtSavedSessionsOnStderrOnly(t *testing.T) {
	home := isolatedDataHome(t)
	snapfixture.CommitOneSession(t, filepath.Join(home, "sesh", "snapshots"))

	stdout, stderr, err := executeCommand(t, "session", "list", testSocketFlag, filepath.Join(home, "absent.sock"), testNoConfigFlag)
	if err != nil {
		t.Fatalf("an empty list must still succeed: %v", err)
	}

	if !strings.Contains(stderr, "1 saved session available.") || !strings.Contains(stderr, "start it on the same socket first") || !strings.Contains(stderr, "Restore with: sesh snapshot restore --socket '"+filepath.Join(home, "absent.sock")+"' --repository '"+filepath.Join(home, "sesh", "snapshots")+"' --no-config") {
		t.Fatalf("expected the restore hint on stderr, got %q", stderr)
	}

	if strings.Contains(stdout, "restore") {
		t.Fatalf("the hint must not appear in the command result: %q", stdout)
	}
}

func TestEmptySessionListIsQuietWithoutSavedSessionsOrInJSON(t *testing.T) {
	home := isolatedDataHome(t)

	_, stderr, err := executeCommand(t, "session", "list", testSocketFlag, filepath.Join(home, "absent.sock"), testNoConfigFlag)
	if err != nil || stderr != "" {
		t.Fatalf("no repository means no hint, got %q, %v", stderr, err)
	}

	snapfixture.CommitOneSession(t, filepath.Join(home, "sesh", "snapshots"))

	exec := executeJSON(t, "session", "list", testSocketFlag, filepath.Join(home, "absent.sock"), testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || strings.Contains(exec.Stderr, "restore") || strings.Contains(exec.Stdout, "can be restored") {
		t.Fatalf("JSON output must carry no hint: stdout=%q stderr=%q err=%v", exec.Stdout, exec.Stderr, exec.Err)
	}
}
