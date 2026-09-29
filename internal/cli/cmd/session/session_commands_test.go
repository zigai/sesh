package cmdsession

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestCommandSocketValidation(t *testing.T) {
	t.Parallel()

	for _, cmdName := range []string{"list", "history"} {
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

	exec := executeJSON(t, cmdName, testNoConfigFlag, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("%s without --socket on a server-less default socket = %v / %+v, want an empty result", cmdName, exec.err, exec.envelope.Error)
	}
}

func verifyRelativeSocketPath(t *testing.T, cmdName string) {
	t.Helper()

	exec := executeJSON(t, cmdName, testSocketFlag, "relative/sock", testNoConfigFlag, testJSONFlag)
	if exec.err == nil {
		t.Fatal("expected error for relative socket, got nil")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
	}

	if exec.envelope.Error == nil || exec.envelope.Error.Code != "endpointRequired" {
		t.Fatalf("error payload = %+v, want code endpointRequired", exec.envelope.Error)
	}

	if !strings.Contains(exec.envelope.Error.Message, "explicit absolute tmux socket is required") {
		t.Fatalf("error message = %q", exec.envelope.Error.Message)
	}
}

func verifyPositionalArgs(t *testing.T, cmdName string) {
	t.Helper()

	exec := executeJSON(t, cmdName, testSocketFlag, "/tmp/valid.sock", "unexpected-arg", testNoConfigFlag, testJSONFlag)
	if exec.err == nil {
		t.Fatal("expected error for positional args, got nil")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
	}
}

func TestListAndHistoryHelpDoesNotRequireSocket(t *testing.T) {
	t.Parallel()

	root := newTestRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "list", "--help")
	if err != nil {
		t.Fatalf("list --help returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("list --help wrote to stderr: %q", stderr)
	}

	if !strings.Contains(stdout, "List live tmux sessions") {
		t.Fatalf("list --help missing expected text: %q", stdout)
	}

	root = newTestRootCommand(t.Context())

	stdout, stderr, err = executeCommand(root, "history", "--help")
	if err != nil {
		t.Fatalf("history --help returned error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("history --help wrote to stderr: %q", stderr)
	}

	if !strings.Contains(stdout, "Show tmux history lines by session") {
		t.Fatalf("history --help missing expected text: %q", stdout)
	}
}

func TestActivityFormatting(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "zero time", at: time.Time{}, want: ""},
		{name: "seconds ago", at: now.Add(-30 * time.Second), want: "now"},
		{name: "minutes ago", at: now.Add(-15 * time.Minute), want: "15m"},
		{name: "hours ago", at: now.Add(-4 * time.Hour), want: "4h"},
		{name: "days ago", at: now.Add(-3 * 24 * time.Hour), want: "3d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatActivityAt(tc.at, now)
			if got != tc.want {
				t.Fatalf("formatActivityAt(%v) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}

func TestListJSONEnvelopeContractEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "list", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.err != nil {
		t.Fatalf("list on nonexistent socket returned error: %v", exec.err)
	}

	if !exec.envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.envelope)
	}

	if exec.envelope.Command != "list" {
		t.Fatalf("command = %q, want list", exec.envelope.Command)
	}

	res := decodeJSONPayload[sessionListJSONResult](t, exec.envelope.Result)
	if res.Sessions == nil {
		t.Fatal("expected non-nil empty slice for sessions, got nil")
	}

	if len(res.Sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(res.Sessions))
	}

	if !strings.Contains(string(exec.envelope.Result), `"sessions": []`) {
		t.Fatalf("result JSON does not contain empty array: %s", string(exec.envelope.Result))
	}

	assertAllKeysCamelCase(t, res, "list.result")
}

func TestHistoryJSONEnvelopeSessionsEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "history", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.err != nil {
		t.Fatalf("history returned error: %v", exec.err)
	}

	if !exec.envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.envelope)
	}

	if exec.envelope.Command != "history" {
		t.Fatalf("command = %q, want history", exec.envelope.Command)
	}

	res := decodeJSONPayload[sessionHistoryJSONResult](t, exec.envelope.Result)
	if res.Mode != "sessions" {
		t.Fatalf("mode = %q, want sessions", res.Mode)
	}

	if res.Sessions == nil || len(res.Sessions) != 0 {
		t.Fatalf("expected empty non-nil sessions, got %+v", res.Sessions)
	}

	if !strings.Contains(string(exec.envelope.Result), `"sessions": []`) {
		t.Fatalf("result JSON missing empty array: %s", string(exec.envelope.Result))
	}

	assertAllKeysCamelCase(t, res, "history.result")
}

func TestHistoryJSONEnvelopePanesEmpty(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	exec := executeJSON(t, "history", "--panes", testSocketFlag, sockPath, testNoConfigFlag, testJSONFlag)

	if exec.err != nil {
		t.Fatalf("history --panes returned error: %v", exec.err)
	}

	if !exec.envelope.OK {
		t.Fatalf("expected OK=true, got %+v", exec.envelope)
	}

	if exec.envelope.Command != "history" {
		t.Fatalf("command = %q, want history", exec.envelope.Command)
	}

	res := decodeJSONPayload[paneHistoryJSONResult](t, exec.envelope.Result)
	if res.Mode != "panes" {
		t.Fatalf("mode = %q, want panes", res.Mode)
	}

	if res.Panes == nil || len(res.Panes) != 0 {
		t.Fatalf("expected empty non-nil panes, got %+v", res.Panes)
	}

	if !strings.Contains(string(exec.envelope.Result), `"panes": []`) {
		t.Fatalf("result JSON missing empty array: %s", string(exec.envelope.Result))
	}

	assertAllKeysCamelCase(t, res, "history_panes.result")
}

func TestListTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	root := newTestRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "list", testSocketFlag, sockPath, testNoConfigFlag)
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

func TestHistorySummaryTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	root := newTestRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "history", testSocketFlag, sockPath, testNoConfigFlag)
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

func TestHistoryPanesTableEmptyOutput(t *testing.T) {
	t.Parallel()

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	root := newTestRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "history", "--panes", testSocketFlag, sockPath, testNoConfigFlag)
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
