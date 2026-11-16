//go:build integration

package session_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

func TestSessionCommandsWithoutSocketTargetDefaultTmuxServer(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	tests := []struct {
		name string
		args []string
	}{
		{name: "create", args: []string{"session", "create", "dev"}},
		{name: "rename", args: []string{"session", "rename", "dev", "renamed"}},
		{name: "kill", args: []string{"session", "kill", "dev"}},
		{name: "delete", args: []string{"session", "delete", "dev"}},
		{name: "switch", args: []string{"session", "switch", "dev"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := executeJSON(t, append(tc.args, testNoConfigFlag, testJSONFlag)...)

			if !errors.Is(exec.Err, gotmux.ErrNoServer) &&
				!errors.Is(exec.Err, session.ErrSessionNotFound) {
				t.Fatalf(
					"error = %v, want the isolated default socket to have no server or session",
					exec.Err,
				)
			}

			if code := clibase.ExitCode(
				exec.Err,
			); code == clibase.ExitCodeUsage ||
				exec.Envelope.OK {
				t.Fatalf(
					"exit code %d / envelope %+v, want a non-usage failure",
					code,
					exec.Envelope,
				)
			}
		})
	}
}

func TestSessionCreateRejectsMissingDirectoryBeforeTmuxWork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	exec := executeJSON(
		t,
		"session", "create", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		"--dir", missing,
		testNoConfigFlag,
		testJSONFlag,
	)

	if exec.Err == nil {
		t.Fatal("expected a missing directory failure")
	}

	if exec.Envelope.OK {
		t.Fatalf("envelope reported success for a missing directory: %+v", exec.Envelope)
	}

	if !strings.Contains(exec.Err.Error(), "directory does not exist") {
		t.Fatalf("error = %v, want directory failure", exec.Err)
	}
}

func TestSessionCreateWithCreateDirRejectsRegularFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"session", "create", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		"--dir", file,
		"--create-dir",
		testNoConfigFlag,
		testJSONFlag,
	)

	if exec.Err == nil || exec.Envelope.OK {
		t.Fatalf("create in a regular file = %v / %+v, want failure", exec.Err, exec.Envelope)
	}

	if !strings.Contains(exec.Err.Error(), "path is not a directory") {
		t.Fatalf("error = %v, want not-a-directory failure", exec.Err)
	}
}

func TestSessionDeletePropagatesRepositoryOpenFailure(t *testing.T) {
	repositoryFile := filepath.Join(t.TempDir(), "not-a-repository")
	if err := os.WriteFile(repositoryFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"session", "delete", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		testRepositoryFlag, repositoryFile,
		testNoConfigFlag,
		testJSONFlag,
	)

	if exec.Err == nil {
		t.Fatal("expected a repository open failure")
	}

	if exec.Envelope.OK {
		t.Fatalf("envelope reported success for an unusable repository: %+v", exec.Envelope)
	}

	if !strings.Contains(exec.Err.Error(), "repository") {
		t.Fatalf("error = %v, want a repository failure", exec.Err)
	}
}
