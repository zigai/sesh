//go:build integration

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCommandsDoNotCreateNonexistentRepository(t *testing.T) {
	tempBase := t.TempDir()
	nonexistent := filepath.Join(tempBase, "does-not-exist")

	commands := [][]string{
		{snapshotCmd, "list", testRepositoryFlag, nonexistent},
		{snapshotCmd, "history", testRepositoryFlag, nonexistent},
		{snapshotCmd, "verify", testRepositoryFlag, nonexistent},
	}

	for _, cmdArgs := range commands {
		t.Run(strings.Join(cmdArgs, " "), func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, cmdArgs...)
			if err == nil {
				t.Fatalf("expected error reading nonexistent repo, got nil")
			}

			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("err %v should wrap os.ErrNotExist", err)
			}

			code := ExitCode(err)
			if code != exitCodeGeneral {
				t.Fatalf("ExitCode = %d, want %d", code, exitCodeGeneral)
			}

			if stdout != "" {
				t.Fatalf("stdout should be empty, got: %q", stdout)
			}

			if stderr == "" {
				t.Fatalf("stderr should contain error message")
			}

			if _, statErr := os.Stat(nonexistent); !os.IsNotExist(statErr) {
				t.Fatalf("read command created nonexistent repository directory: %v", statErr)
			}
		})
	}
}
