package git_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/git"
)

func TestFailedCommandReportsGitsErrorLineAfterProgressOutput(t *testing.T) {
	repo := t.TempDir()

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
		{"branch", "taken"},
	} {
		if _, err := git.Output(t.Context(), repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	_, err := git.Output(t.Context(), repo, "worktree", "add", "-b", "taken", filepath.Join(t.TempDir(), "tree"))

	commandErr, ok := errors.AsType[*git.CommandError](err)
	if !ok {
		t.Fatalf("Output() error = %v, want a CommandError", err)
	}

	if want := "fatal: a branch named 'taken' already exists"; commandErr.Detail != want {
		t.Fatalf("Detail = %q, want %q", commandErr.Detail, want)
	}
}
