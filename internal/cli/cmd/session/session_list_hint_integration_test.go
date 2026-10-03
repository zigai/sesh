//go:build (linux || darwin) && integration

package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestSessionListNamesSavedSessionsANewerServerLacks(t *testing.T) {
	home := isolatedDataHome(t)
	repoPath := filepath.Join(home, "sesh", "snapshots")
	snapfixture.CommitOneSession(t, repoPath)

	server := tmuxtest.NewServer(t)
	socket := server.Endpoint().SocketPath

	stdout, stderr, err := executeCommand(t, "session", "list", testSocketFlag, socket, testNoConfigFlag)
	if err != nil {
		t.Fatalf("session list: %v\nstderr: %s", err, stderr)
	}

	want := "Saved sessions not running: work\n" +
		"tmux started after the newest snapshot, so they were not restored.\n" +
		"Restore with: sesh snapshot restore --socket '" + socket + "' --repository '" + repoPath + "' --no-config\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}

	if strings.Contains(stdout, "restore") {
		t.Fatalf("the hint must not appear in the command result: %q", stdout)
	}

	exec := executeJSON(t, "session", "list", testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || exec.Stderr != "" {
		t.Fatalf("JSON output must carry no hint: stderr=%q err=%v", exec.Stderr, exec.Err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "work",
		Program: gotmux.Exec("/bin/sh"),
		Start:   gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatalf("recreate the saved session: %v", err)
	}

	if _, stderr, err := executeCommand(t, "session", "list", testSocketFlag, socket, testNoConfigFlag); err != nil || stderr != "" {
		t.Fatalf("once the saved sessions run, the list must be quiet: %q, %v", stderr, err)
	}
}
