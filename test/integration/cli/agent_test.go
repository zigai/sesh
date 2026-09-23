//go:build (linux || darwin) && integration

package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/session"
)

type agentNavResultJSON struct {
	TargetPaneID string `json:"targetPaneId"`
}

func writeAgentRegistry(t *testing.T, sessions ...string) string {
	t.Helper()

	document := `{"schema_version": 2, "revision": 1, "sessions": {` + strings.Join(sessions, ",") + `}}`

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

type agentIntegrationFixture struct {
	server    *gotmux.Server
	socket    string
	statePath string
	current   agents.PaneLocation
	target    agents.PaneLocation
}

func newAgentIntegrationFixture(t *testing.T) agentIntegrationFixture {
	t.Helper()

	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("fixture sessions = %d, err = %v", len(sessions), err)
	}

	panes, err := agents.ObservePanes(ctx, server)
	if err != nil || len(panes) == 0 {
		t.Fatalf("fixture panes: %v", err)
	}

	current := panes[0]

	window, err := sessions[0].Handle().NewWindow(ctx, gotmux.NewWindowOptions{
		Name:    "agent-target",
		Dir:     "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		Select:  false,
	})
	if err != nil {
		t.Fatalf("creating agent window: %v", err)
	}

	pane, err := window.Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("agent window pane: %v", err)
	}

	target := livePane(t, ctx, server, string(pane.ID()))

	statePath := writeAgentRegistry(t, agentRegistrySessionForPane(target, "waiting"))

	return agentIntegrationFixture{
		server:    server,
		socket:    server.Endpoint().SocketPath,
		statePath: statePath,
		current:   current,
		target:    target,
	}
}

func livePane(t *testing.T, ctx context.Context, server *gotmux.Server, paneID string) agents.PaneLocation {
	t.Helper()

	panes, err := agents.ObservePanes(ctx, server)
	if err != nil {
		t.Fatalf("observing panes: %v", err)
	}

	for _, pane := range panes {
		if pane.PaneID == paneID {
			return pane
		}
	}

	t.Fatalf("pane %s is not live", paneID)

	return agents.PaneLocation{}
}

func agentRegistrySessionForPane(pane agents.PaneLocation, activity string) string {
	return `"agent-` + pane.PaneID + `": {
		"schema_version": 2,
		"id": "agent-` + pane.PaneID + `",
		"harness": "omp",
		"presence": "live",
		"activity": "` + activity + `",
		"observations": {},
		"created_at": "2026-09-20T10:00:00Z",
		"updated_at": "2026-09-20T10:00:00Z",
		"presence_changed_at": "2026-09-20T10:00:00Z",
		"activity_changed_at": "2026-09-20T10:00:00Z",
		"tmux": {"inside": true, "server_socket": "` + pane.ServerSocket + `", "session_id": "` + pane.SessionID + `", "session_name": "` + pane.SessionName + `", "window_id": "` + pane.WindowID + `", "pane_id": "` + pane.PaneID + `", "pane_pid": ` + strconv.Itoa(pane.PanePID) + `}
	}`
}

func activeWindow(t *testing.T, ctx context.Context, server *gotmux.Server, sessionID string) string {
	t.Helper()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("session windows: %v", err)
	}

	for _, link := range links {
		if link.Active {
			return string(link.WindowID)
		}
	}

	return ""
}

func TestCLIAgentNextActivatesAgentWindow(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	root := cli.NewRootCommand(t.Context())

	historyDir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(historyDir, "history.json")
	stdout, stderr, err := executeCommand(root, actionCmd, "agent-next",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--state", fixture.statePath,
		"--pane", fixture.current.PaneID, "--history", historyPath, testJSONFlag)
	if err != nil {
		t.Fatalf("agent next failed: %v\nstderr: %s", err, stderr)
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding navigation envelope %q: %v", stdout, err)
	}

	result := decodeJSONPayload[agentNavResultJSON](t, envelope.Result)

	if result.TargetPaneID != fixture.target.PaneID {
		t.Fatalf("target pane = %q, want %q", result.TargetPaneID, fixture.target.PaneID)
	}

	if got := activeWindow(t, ctx, fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
		t.Fatalf("active window = %q, want %q", got, fixture.target.WindowID)
	}

	if got := livePane(t, ctx, fixture.server, fixture.target.PaneID); !got.PaneActive {
		t.Fatalf("pane %s is not the active pane after navigation", fixture.target.PaneID)
	}

	st, err := session.LoadState(historyPath)
	if err != nil {
		t.Fatalf("loading state after agent-next: %v", err)
	}
	ident := session.Identity{Origin: "local", Socket: fixture.target.ServerSocket, Name: fixture.target.SessionName}
	if got := st.GetLastAgentView(ident, fixture.target.PaneID); got == 0 {
		t.Fatal("agent-next did not record agent view in history")
	}

	root = cli.NewRootCommand(t.Context())

	stdout, stderr, err = executeCommand(root, actionCmd, "agent-back",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--state", fixture.statePath, "--pane", fixture.target.PaneID, testJSONFlag)
	if err != nil {
		t.Fatalf("agent back failed: %v\nstderr: %s", err, stderr)
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding back envelope %q: %v", stdout, err)
	}

	result = decodeJSONPayload[agentNavResultJSON](t, envelope.Result)

	if result.TargetPaneID != fixture.current.PaneID {
		t.Fatalf("back target pane = %q, want %q", result.TargetPaneID, fixture.current.PaneID)
	}

	if got := activeWindow(t, ctx, fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("active window after back = %q, want %q", got, fixture.current.WindowID)
	}
}

func TestCLIAgentFocusActivatesNamedPane(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	root := cli.NewRootCommand(t.Context())

	_, stderr, err := executeCommand(root, actionCmd, "agent-focus",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--state", fixture.statePath,
		"--pane", fixture.current.PaneID, "--target-pane", fixture.target.PaneID)
	if err != nil {
		t.Fatalf("agent focus failed: %v\nstderr: %s", err, stderr)
	}

	if got := activeWindow(t, ctx, fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
		t.Fatalf("active window = %q, want %q", got, fixture.target.WindowID)
	}

	if got := livePane(t, ctx, fixture.server, fixture.target.PaneID); !got.PaneActive {
		t.Fatalf("focused pane %s is not active", fixture.target.PaneID)
	}
}

func TestCLIAgentStatusReadsRegistry(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	root := cli.NewRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, actionCmd, "agent-status", testNoConfigFlag, "--state", fixture.statePath)
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Agents: 1 live") || !strings.Contains(stdout, "waiting: 1") {
		t.Fatalf("unexpected status output: %q", stdout)
	}
}
