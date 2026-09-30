package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

type agentStatusResultJSON struct {
	Total   int `json:"total"`
	Live    int `json:"live"`
	Waiting int `json:"waiting"`
	Failed  int `json:"failed"`
	Idle    int `json:"idle"`
	Unread  int `json:"unread"`
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	exec := clitest.Execute(t, args...)

	return exec.Stdout, exec.Stderr, exec.Err
}

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

func TestAgentCommandsHelp(t *testing.T) {
	t.Parallel()

	commands := [][]string{
		{"action", "agent-next"},
		{"action", "agent-latest"},
		{"action", "agent-waiting"},
		{"action", "agent-failed"},
		{"action", "agent-unread"},
		{"action", "agent-toggle"},
		{"action", "agent-back"},
		{"action", "agent-focus"},
		{"action", "agent-status"},
		{"action", "agent-window-status-clear"},
		{"daemon", "agent-window-status"},
	}

	for _, command := range commands {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			t.Parallel()

			stdout, stderr, err := executeCommand(t, append(command, "--help")...)
			if err != nil {
				t.Fatalf("%s --help failed: %v\nstderr: %s", strings.Join(command, " "), err, stderr)
			}

			if !strings.Contains(stdout, "sesh "+strings.Join(command, " ")) {
				t.Fatalf("stdout missing %s in help: %s", strings.Join(command, " "), stdout)
			}
		})
	}
}

func TestAgentNavigationWithoutSocketTargetsDefaultTmuxServer(t *testing.T) {
	for _, action := range []string{"agent-next", "agent-latest", "agent-back"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("TMUX", "")

			_, _, err := executeCommand(t, "action", action, "--no-config")
			if !errors.Is(err, gotmux.ErrNoServer) {
				t.Fatalf("error = %v, want the isolated default socket to report no tmux server", err)
			}

			if code := clibase.ExitCode(err); code != clibase.ExitCodeGeneral {
				t.Fatalf("exit code = %d, want %d", code, clibase.ExitCodeGeneral)
			}
		})
	}
}

func TestAgentFocusRequiresTargetPane(t *testing.T) {
	t.Setenv("TMUX", "")

	_, _, err := executeCommand(t, "action", "agent-focus", "--no-config", "--socket", "/tmp/does-not-exist.sock")
	if err == nil {
		t.Fatal("expected usage error without a target pane, got nil")
	}

	if code := clibase.ExitCode(err); code != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", code, clibase.ExitCodeUsage)
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

	stdout, stderr, err := executeCommand(t, "action", "agent-status", "--no-config", "--state", statePath, "--json")
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	var envelope clitest.Envelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding status envelope %q: %v", stdout, err)
	}

	if !envelope.OK {
		t.Fatalf("status envelope is not ok: %s", stdout)
	}

	result := clitest.DecodePayload[agentStatusResultJSON](t, envelope.Result)

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

	stdout, stderr, err := executeCommand(t, "action", "agent-status", "--no-config", "--state", statePath)
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

	_, _, err := executeCommand(t, "action", "agent-status", "--no-config", "--state", statePath)
	if err == nil {
		t.Fatal("expected failure on an unsupported registry schema")
	}
}

func writeAgentStoreConfig(t *testing.T, storePath string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[agent_status]\nstore_path = "+strconv.Quote(storePath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func agentStatusTotal(t *testing.T, args ...string) int {
	t.Helper()

	stdout, stderr, err := executeCommand(t, append([]string{"action", "agent-status", "--json"}, args...)...)
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	var envelope clitest.Envelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding status envelope %q: %v", stdout, err)
	}

	return clitest.DecodePayload[agentStatusResultJSON](t, envelope.Result).Total
}

func TestAgentStatusReadsConfiguredRegistryWithoutStateFlag(t *testing.T) {
	t.Parallel()

	configured := writeAgentRegistry(t,
		agentRegistrySession("first-agent", "waiting", "%1"),
		agentRegistrySession("second-agent", "idle", "%2"),
	)

	if total := agentStatusTotal(t, "--config", writeAgentStoreConfig(t, configured)); total != 2 {
		t.Fatalf("status total = %d, want 2 agents from [agent_status].store_path", total)
	}
}

func TestAgentStatusStateFlagOverridesConfiguredRegistry(t *testing.T) {
	t.Parallel()

	configured := writeAgentRegistry(t, agentRegistrySession("configured-agent", "waiting", "%1"))
	explicit := writeAgentRegistry(t,
		agentRegistrySession("first-agent", "waiting", "%1"),
		agentRegistrySession("second-agent", "idle", "%2"),
		agentRegistrySession("third-agent", "failed", "%3"),
	)

	if total := agentStatusTotal(t, "--config", writeAgentStoreConfig(t, configured), "--state", explicit); total != 3 {
		t.Fatalf("status total = %d, want 3 agents from --state", total)
	}
}
