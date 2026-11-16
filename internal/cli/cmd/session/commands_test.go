package session_test

import (
	"strings"
	"testing"
)

func TestListAndScrollbackHelpDoesNotRequireSocket(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, "session", "list", "--help")
	if err != nil {
		t.Fatalf("list --help returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("list --help wrote to stderr: %q", stderr)
	}

	if !strings.Contains(stdout, "List live tmux sessions") {
		t.Fatalf("list --help missing expected text: %q", stdout)
	}

	stdout, stderr, err = executeCommand(t, "session", "scrollback", "--help")
	if err != nil {
		t.Fatalf("scrollback --help returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("scrollback --help wrote to stderr: %q", stderr)
	}

	if !strings.Contains(stdout, "Show tmux scrollback line counts by session") {
		t.Fatalf("scrollback --help missing expected text: %q", stdout)
	}
}
