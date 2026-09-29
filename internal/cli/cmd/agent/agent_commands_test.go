package cmdagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func writeAgentRegistry(t *testing.T, sessions ...string) string {
	t.Helper()

	document := `{"schema_version": 3, "revision": 1, "sessions": {` + strings.Join(sessions, ",") + `}}`

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func agentRegistrySession(id string, activity string, paneID string) string {
	return `"` + id + `": {
		"schema_version": 3,
		"id": "` + id + `",
		"harness": "omp",
		"liveness": {"kind": "live", "activity": "` + activity + `"},
		"observations": {},
		"created_at": "2026-09-20T10:00:00Z",
		"updated_at": "2026-09-20T10:00:00Z",
		"presence_changed_at": "2026-09-20T10:00:00Z",
		"activity_changed_at": "2026-09-20T10:00:00Z",
		"location": {"kind": "tmux", "server_id": "/tmp/fixture/socket", "session_id": "$1", "session_name": "dev", "window_id": "@1", "pane_id": "` + paneID + `", "pane_pid": 4242}
	}`
}

func TestActionAgentCommandsHelp(t *testing.T) {
	t.Parallel()

	commands := [][]string{
		{"agent-next"},
		{"agent-back"},
		{"agent-focus"},
		{"agent-status"},
		{"agent-status", "run"},
		{"agent-status", "clear"},
	}

	for _, sub := range commands {
		t.Run(strings.Join(sub, " "), func(t *testing.T) {
			t.Parallel()

			root := newRootCommand(t.Context())

			args := append([]string{actionCmd}, sub...)
			args = append(args, "--help")

			stdout, stderr, err := executeCommand(root, args...)
			if err != nil {
				t.Fatalf("action %s --help failed: %v\nstderr: %s", strings.Join(sub, " "), err, stderr)
			}

			if !strings.Contains(stdout, sub[len(sub)-1]) {
				t.Fatalf("stdout missing %s in help: %s", sub[len(sub)-1], stdout)
			}
		})
	}
}

func TestAgentNavigationWithoutSocketTargetsDefaultTmuxServer(t *testing.T) {
	for _, action := range []string{"agent-next", "agent-back"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("TMUX", "")

			root := newRootCommand(t.Context())

			_, _, err := executeCommand(root, actionCmd, action, "--no-config")
			if !errors.Is(err, gotmux.ErrNoServer) {
				t.Fatalf("error = %v, want the isolated default socket to report no tmux server", err)
			}

			if code := ExitCode(err); code != clibase.ExitCodeGeneral {
				t.Fatalf("exit code = %d, want %d", code, clibase.ExitCodeGeneral)
			}
		})
	}
}

func TestAgentFocusRequiresTargetPane(t *testing.T) {
	t.Setenv("TMUX", "")

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, actionCmd, "agent-focus", "--no-config", "--socket", "/tmp/does-not-exist.sock")
	if err == nil {
		t.Fatal("expected usage error without a target pane, got nil")
	}

	if code := ExitCode(err); code != exitCodeUsage {
		t.Fatalf("exit code = %d, want %d", code, exitCodeUsage)
	}
}

func TestAgentStatusSummaryJSON(t *testing.T) {
	t.Parallel()

	statePath := writeAgentRegistry(
		t,
		agentRegistrySession("waiting-agent", "waiting", "%1"),
		agentRegistrySession("failed-agent", "failed", "%2"),
		agentRegistrySession("idle-agent", "idle", "%3"),
	)

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, actionCmd, "agent-status", "--no-config", "--state", statePath, testJSONFlag)
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding status envelope %q: %v", stdout, err)
	}

	if !envelope.OK {
		t.Fatalf("status envelope is not ok: %s", stdout)
	}

	result := decodeJSONPayload[agentStatusResultJSON](t, envelope.Result)

	if result.Total != 3 || result.Live != 3 {
		t.Fatalf("status total/live = %d/%d, want 3/3", result.Total, result.Live)
	}

	if result.Waiting != 1 || result.Failed != 1 || result.Idle != 1 {
		t.Fatalf("status attention counts = %+v", result)
	}

	if result.Unread != 0 {
		t.Fatalf("registry status must report zero unread, got %d", result.Unread)
	}
}

func TestAgentStatusSummaryText(t *testing.T) {
	t.Parallel()

	statePath := writeAgentRegistry(t, agentRegistrySession("waiting-agent", "waiting", "%1"))

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, actionCmd, "agent-status", "--no-config", "--state", statePath)
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Agents: 1 live") || !strings.Contains(stdout, "waiting: 1") {
		t.Fatalf("unexpected status output: %q", stdout)
	}
}

func TestAgentStatusReportsUnsupportedRegistry(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"schema_version": 99, "sessions": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, actionCmd, "agent-status", "--no-config", "--state", statePath)
	if err == nil {
		t.Fatal("expected failure on an unsupported registry schema")
	}
}
