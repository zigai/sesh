package git_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	_, err := git.Output(
		t.Context(),
		repo,
		"worktree",
		"add",
		"-b",
		"taken",
		filepath.Join(t.TempDir(), "tree"),
	)

	commandErr, ok := errors.AsType[*git.CommandError](err)
	if !ok {
		t.Fatalf("Output() error = %v, want a CommandError", err)
	}

	if want := "fatal: a branch named 'taken' already exists"; commandErr.Detail != want {
		t.Fatalf("Detail = %q, want %q", commandErr.Detail, want)
	}
}

func TestEachLineRefusesASingleLineLongerThanTheOutputLimit(t *testing.T) {
	repo := t.TempDir()
	message := filepath.Join(t.TempDir(), "message")

	if err := os.WriteFile(message, []byte(strings.Repeat("a", 2<<20)), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{
			"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
			"commit", "-q", "--no-verify", "--allow-empty", "-F", message,
		},
	} {
		if _, err := git.Output(t.Context(), repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	var lines int

	err := git.EachLine(t.Context(), repo, func(string) { lines++ }, "log", "-1", "--format=%s")
	if !errors.Is(err, git.ErrOutputTooLarge) {
		t.Fatalf("EachLine() error = %v, want ErrOutputTooLarge", err)
	}

	if lines != 0 {
		t.Fatalf("EachLine() delivered %d lines, want none", lines)
	}
}
