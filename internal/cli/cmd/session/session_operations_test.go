package cmdsession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

var errTestHistoryList = errors.New("test history list failure")

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

func TestSessionCommandsWithoutSocketTargetDefaultTmuxServer(t *testing.T) {
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

			// Without a server, lookups find no session and mutations report the missing server.
			if !errors.Is(exec.err, gotmux.ErrNoServer) && !errors.Is(exec.err, session.ErrSessionNotFound) {
				t.Fatalf("error = %v, want the isolated default socket to have no server or session", exec.err)
			}

			if code := clibase.ExitCode(exec.err); code == clibase.ExitCodeUsage || exec.envelope.OK {
				t.Fatalf("exit code %d / envelope %+v, want a non-usage failure", code, exec.envelope)
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

func TestResolveSessionDirectoryErrorWrapping(t *testing.T) {
	t.Parallel()

	t.Run("directory not found wraps oops", func(t *testing.T) {
		t.Parallel()

		cmd := NewActionSessionNewCommand()
		missing := filepath.Join(t.TempDir(), "nonexistent")
		_ = cmd.Flags().Set("dir", missing)

		_, err := resolveSessionDirectory(cmd)
		if !errors.Is(err, errDirectoryNotFound) {
			t.Fatalf("expected errDirectoryNotFound, got %v", err)
		}

		oopsErr, ok := oops.AsOops(err)
		if !ok {
			t.Fatalf("expected oops error, got %T", err)
		}

		if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "stat_directory" {
			t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
		}
	})

	t.Run("directory not a dir wraps oops", func(t *testing.T) {
		t.Parallel()

		filePath := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := NewActionSessionNewCommand()
		_ = cmd.Flags().Set("dir", filePath)

		_, err := resolveSessionDirectory(cmd)
		if !errors.Is(err, errDirectoryNotDir) {
			t.Fatalf("expected errDirectoryNotDir, got %v", err)
		}

		oopsErr, ok := oops.AsOops(err)
		if !ok {
			t.Fatalf("expected oops error, got %T", err)
		}

		if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "stat_directory" {
			t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
		}
	})
}

func TestWrapHistoryListError(t *testing.T) {
	t.Parallel()

	if err := wrapHistoryListError(nil); err != nil {
		t.Fatalf("expected nil for nil error, got %v", err)
	}

	wrapped := wrapHistoryListError(errTestHistoryList)
	if !errors.Is(wrapped, errTestHistoryList) {
		t.Fatalf("expected errors.Is match, got %v", wrapped)
	}

	oopsErr, ok := oops.AsOops(wrapped)
	if !ok {
		t.Fatalf("expected oops error, got %T", wrapped)
	}

	if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "history" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
	}

	again := wrapHistoryListError(wrapped)

	againErr, ok := oops.AsOops(again)
	if !ok || len(againErr.Layers()) != len(oopsErr.Layers()) || again.Error() != wrapped.Error() {
		t.Fatalf("expected idempotent return of oops error, got %v", again)
	}
}
