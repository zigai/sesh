package setup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/setup"
)

func TestSetupCommandHelpListsOwnedSubcommands(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, "setup", "--help")
	if err != nil {
		t.Fatalf("setup --help failed: %v\nstderr: %s", err, stderr)
	}

	for _, subcommand := range []string{"tmux", "agents", "services"} {
		if !strings.Contains(stdout, subcommand) {
			t.Fatalf("setup help is missing %q:\n%s", subcommand, stdout)
		}
	}
}

func TestSetupServicesTmuxServiceRequiresExplicitTmuxPath(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services",
		"--with-tmux-service",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err == nil {
		t.Fatal("the managed tmux service requires an explicit tmux path")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeUsage)
	}
}

func TestSetupAgentsRejectsUnknownNames(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(t, "setup", "agents", "not-a-real-agent", "--dry-run", testNoConfigFlag, testJSONFlag)
	if exec.Err == nil {
		t.Fatal("an unknown agent name must fail")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeUsage)
	}
}

func TestSetupAgentsRejectsUnknownTrackerMode(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(t, "setup", "agents", "--tracker", "bogus", "--dry-run", testNoConfigFlag, testJSONFlag)
	if exec.Err == nil {
		t.Fatal("an unknown tracker operation must fail")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeUsage)
	}
}

func createFakeTmuxExecutable(t *testing.T) string {
	t.Helper()

	tmuxPath := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(tmuxPath, 0o700); err != nil {
		t.Fatal(err)
	}

	return tmuxPath
}

func TestSetupServicesTmuxServiceSymlinkProtection(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(t.TempDir(), "external-target.service")
	targetContent := []byte("[Unit]\nDescription=external\n")

	if err := os.WriteFile(targetPath, targetContent, 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(directory, setup.TmuxServiceName)
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err == nil {
		t.Fatal("symlink target must be reported as foreign/skipped")
	}

	curTarget, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(curTarget) != string(targetContent) {
		t.Fatalf("symlink target was overwritten through symlink:\n%s", curTarget)
	}
}
