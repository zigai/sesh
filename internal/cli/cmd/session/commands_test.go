package session_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

type (
	listResult struct {
		Sessions []any `json:"sessions"`
	}
	historyResult struct {
		Mode     string `json:"mode"`
		Sessions []any  `json:"sessions"`
	}
	paneHistoryResult struct {
		Mode  string `json:"mode"`
		Panes []any  `json:"panes"`
	}
)

func TestCommandSocketValidation(t *testing.T) {
	t.Parallel()

	for _, cmdName := range []string{"session list", "session scrollback"} {
		t.Run(cmdName, func(t *testing.T) {
			verifyCommandSocketValidation(t, cmdName)
		})
	}
}

func verifyCommandSocketValidation(t *testing.T, cmdName string) {
	t.Helper()

	t.Run("default socket without server", func(t *testing.T) {
		verifyDefaultSocketWithoutServer(t, cmdName)
	})

	t.Run("relative socket path", func(t *testing.T) {
		verifyRelativeSocketPath(t, cmdName)
	})

	t.Run("positional arguments rejected", func(t *testing.T) {
		verifyPositionalArgs(t, cmdName)
	})
}

func verifyDefaultSocketWithoutServer(t *testing.T, cmdName string) {
	t.Helper()

	exec := executeJSON(t, append(strings.Fields(cmdName), testNoConfigFlag, testJSONFlag)...)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("%s without --socket on a server-less default socket = %v / %+v, want an empty result", cmdName, exec.Err, exec.Envelope.Error)
	}
}

func verifyRelativeSocketPath(t *testing.T, cmdName string) {
	t.Helper()

	exec := executeJSON(t, append(strings.Fields(cmdName), testSocketFlag, "relative/sock", testNoConfigFlag, testJSONFlag)...)
	if exec.Err == nil {
		t.Fatal("expected error for relative socket, got nil")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeUsage)
	}

	if exec.Envelope.Error == nil || exec.Envelope.Error.Code != "endpointRequired" {
		t.Fatalf("error payload = %+v, want code endpointRequired", exec.Envelope.Error)
	}

	if !strings.Contains(exec.Envelope.Error.Message, "explicit absolute tmux socket is required") {
		t.Fatalf("error message = %q", exec.Envelope.Error.Message)
	}
}

func verifyPositionalArgs(t *testing.T, cmdName string) {
	t.Helper()

	exec := executeJSON(t, append(strings.Fields(cmdName), testSocketFlag, "/tmp/valid.sock", "unexpected-arg", testNoConfigFlag, testJSONFlag)...)
	if exec.Err == nil {
		t.Fatal("expected error for positional args, got nil")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeUsage)
	}
}

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

func TestListJSONEnvelopeContractEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "session", "list", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.Err != nil {
		t.Fatalf("list on nonexistent socket returned error: %v", exec.Err)
	}

	if !exec.Envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.Envelope)
	}

	if exec.Envelope.Command != "session.list" {
		t.Fatalf("command = %q, want session.list", exec.Envelope.Command)
	}

	res := clitest.DecodePayload[listResult](t, exec.Envelope.Result)
	if res.Sessions == nil {
		t.Fatal("expected non-nil empty slice for sessions, got nil")
	}

	if len(res.Sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(res.Sessions))
	}

	if !strings.Contains(string(exec.Envelope.Result), `"sessions": []`) {
		t.Fatalf("result JSON does not contain empty array: %s", string(exec.Envelope.Result))
	}

	assertAllKeysCamelCase(t, clitest.DecodePayload[map[string]any](t, exec.Envelope.Result), "list.result")
}

func TestScrollbackJSONEnvelopeSessionsEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "session", "scrollback", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.Err != nil {
		t.Fatalf("history returned error: %v", exec.Err)
	}

	if !exec.Envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.Envelope)
	}

	if exec.Envelope.Command != "session.scrollback" {
		t.Fatalf("command = %q, want session.scrollback", exec.Envelope.Command)
	}

	res := clitest.DecodePayload[historyResult](t, exec.Envelope.Result)
	if res.Mode != "sessions" {
		t.Fatalf("mode = %q, want sessions", res.Mode)
	}

	if res.Sessions == nil || len(res.Sessions) != 0 {
		t.Fatalf("expected empty non-nil sessions, got %+v", res.Sessions)
	}

	if !strings.Contains(string(exec.Envelope.Result), `"sessions": []`) {
		t.Fatalf("result JSON missing empty array: %s", string(exec.Envelope.Result))
	}

	assertAllKeysCamelCase(t, clitest.DecodePayload[map[string]any](t, exec.Envelope.Result), "history.result")
}

func TestScrollbackJSONEnvelopePanesEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "session", "scrollback", "--panes", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.Err != nil {
		t.Fatalf("history --panes returned error: %v", exec.Err)
	}

	if !exec.Envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.Envelope)
	}

	if exec.Envelope.Command != "session.scrollback" {
		t.Fatalf("command = %q, want session.scrollback", exec.Envelope.Command)
	}

	res := clitest.DecodePayload[paneHistoryResult](t, exec.Envelope.Result)
	if res.Mode != "panes" {
		t.Fatalf("mode = %q, want panes", res.Mode)
	}

	if res.Panes == nil || len(res.Panes) != 0 {
		t.Fatalf("expected empty non-nil panes, got %+v", res.Panes)
	}

	if !strings.Contains(string(exec.Envelope.Result), `"panes": []`) {
		t.Fatalf("result JSON missing empty array: %s", string(exec.Envelope.Result))
	}

	assertAllKeysCamelCase(t, clitest.DecodePayload[map[string]any](t, exec.Envelope.Result), "history_panes.result")
}

func TestListTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	stdout, stderr, err := executeCommand(t, "session", "list", testSocketFlag, sockPath, testNoConfigFlag)
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if stdout != "No results.\n" {
		t.Fatalf("empty list = %q, want empty-state notice", stdout)
	}
}

func TestScrollbackSummaryTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	stdout, stderr, err := executeCommand(t, "session", "scrollback", testSocketFlag, sockPath, testNoConfigFlag)
	if err != nil {
		t.Fatalf("history returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if stdout != "No results.\n" {
		t.Fatalf("empty history = %q, want empty-state notice", stdout)
	}
}

func TestScrollbackPanesTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	stdout, stderr, err := executeCommand(t, "session", "scrollback", "--panes", testSocketFlag, sockPath, testNoConfigFlag)
	if err != nil {
		t.Fatalf("history --panes returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if stdout != "No results.\n" {
		t.Fatalf("empty pane history = %q, want empty-state notice", stdout)
	}
}
