package cmdsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestActionSessionCommandsHelp(t *testing.T) {
	t.Parallel()

	actions := []string{
		"session-new",
		"session-rename",
		"session-kill",
		"session-delete",
		"session-switch",
	}

	for _, actionName := range actions {
		t.Run(actionName, func(t *testing.T) {
			root := newTestRootCommand(t.Context())

			stdout, stderr, err := executeCommand(root, "action", actionName, "--help")
			if err != nil {
				t.Fatalf("action %s --help failed: %v\nstderr: %s", actionName, err, stderr)
			}

			if !strings.Contains(stdout, actionName) {
				t.Fatalf("stdout missing %s in help: %s", actionName, stdout)
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
		{name: "action new missing name", args: []string{"action", "session-new"}, wantCode: clibase.ExitCodeUsage},
		{name: "action rename missing name", args: []string{"action", "session-rename"}, wantCode: clibase.ExitCodeUsage},
		{name: "action switch missing target", args: []string{"action", "session-switch"}, wantCode: clibase.ExitCodeUsage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestRootCommand(t.Context())

			_, _, err := executeCommand(root, tc.args...)
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

func TestSessionCommandsRequireExplicitOrAmbientSocket(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	tests := []struct {
		name string
		args []string
	}{
		{name: "new", args: []string{"action", "session-new", "dev"}},
		{name: "rename", args: []string{"action", "session-rename", "renamed", "--session", "dev"}},
		{name: "kill", args: []string{"action", "session-kill", "dev"}},
		{name: "delete", args: []string{"action", "session-delete", "dev"}},
		{name: "switch", args: []string{"action", "session-switch", "dev"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := executeJSON(t, append(tc.args, testNoConfigFlag, testJSONFlag)...)

			if exec.err == nil {
				t.Fatal("expected a missing-endpoint failure")
			}

			if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
				t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
			}

			if exec.envelope.OK {
				t.Fatalf("envelope reported success without an endpoint: %+v", exec.envelope)
			}

			if !strings.Contains(exec.err.Error(), "--socket is required") {
				t.Fatalf("error = %v, want explicit socket requirement", exec.err)
			}
		})
	}
}

func TestSessionNewRejectsMissingDirectoryBeforeTmuxWork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	exec := executeJSON(
		t,
		"action", "session-new", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		"--dir", missing,
		testNoConfigFlag,
		testJSONFlag,
	)

	if exec.err == nil {
		t.Fatal("expected a missing directory failure")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success for a missing directory: %+v", exec.envelope)
	}

	if !strings.Contains(exec.err.Error(), "directory does not exist") {
		t.Fatalf("error = %v, want directory failure", exec.err)
	}
}

func TestSessionDeletePropagatesRepositoryOpenFailure(t *testing.T) {
	repositoryFile := filepath.Join(t.TempDir(), "not-a-repository")
	if err := os.WriteFile(repositoryFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"action", "session-delete", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		testRepositoryFlag, repositoryFile,
		testNoConfigFlag,
		testJSONFlag,
	)

	if exec.err == nil {
		t.Fatal("expected a repository open failure")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success for an unusable repository: %+v", exec.envelope)
	}

	if !strings.Contains(exec.err.Error(), "repository") {
		t.Fatalf("error = %v, want a repository failure", exec.err)
	}
}

func TestSessionCommandsHonorConfigurationFailures(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("unknown_section = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"action", "session-switch", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		testConfigFlag, configPath,
		testJSONFlag,
	)

	if exec.err == nil {
		t.Fatal("expected a configuration failure")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success with invalid configuration: %+v", exec.envelope)
	}

	if exec.envelope.Error.Code != "configurationFailed" {
		t.Fatalf("error code = %q, want configurationFailed", exec.envelope.Error.Code)
	}
}
