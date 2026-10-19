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

func TestSessionCommandsHelp(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"new", "rename", "kill", "delete", "switch"} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, "session", name, "--help")
			if err != nil {
				t.Fatalf("session %s --help failed: %v\nstderr: %s", name, err, stderr)
			}

			if !strings.Contains(stdout, "sesh session "+name) {
				t.Fatalf("stdout missing session %s in help: %s", name, stdout)
			}
		})
	}
}

func TestSessionCommandsMissingArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "new missing name", args: []string{"session", "new"}, wantCode: clibase.ExitCodeUsage},
		{name: "rename missing name", args: []string{"session", "rename"}, wantCode: clibase.ExitCodeUsage},
		{name: "switch missing target", args: []string{"session", "switch"}, wantCode: clibase.ExitCodeUsage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := executeCommand(t, tc.args...)
			if err == nil {
				t.Fatalf("%s expected error, got nil", tc.name)
			}

			code := clibase.ExitCode(err)
			if code != tc.wantCode {
				t.Fatalf("%s exit code = %d, want %d", tc.name, code, tc.wantCode)
			}
		})
	}
}

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

			if !errors.Is(exec.Err, gotmux.ErrNoServer) && !errors.Is(exec.Err, session.ErrSessionNotFound) {
				t.Fatalf("error = %v, want the isolated default socket to have no server or session", exec.Err)
			}

			if code := clibase.ExitCode(exec.Err); code == clibase.ExitCodeUsage || exec.Envelope.OK {
				t.Fatalf("exit code %d / envelope %+v, want a non-usage failure", code, exec.Envelope)
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

func TestSessionCommandsHonorConfigurationFailures(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("unknown_section = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"session", "switch", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		testConfigFlag, configPath,
		testJSONFlag,
	)

	if exec.Err == nil {
		t.Fatal("expected a configuration failure")
	}

	if exec.Envelope.OK {
		t.Fatalf("envelope reported success with invalid configuration: %+v", exec.Envelope)
	}

	if exec.Envelope.Error.Code != "configurationFailed" {
		t.Fatalf("error code = %q, want configurationFailed", exec.Envelope.Error.Code)
	}
}
