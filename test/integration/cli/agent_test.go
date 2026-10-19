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
	"github.com/zigai/sesh/internal/session"
)

type agentNavResultJSON struct {
	TargetPaneID string `json:"pane"`
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
		"schema_version": 3,
		"id": "agent-` + pane.PaneID + `",
		"harness": "omp",
		"liveness": {"kind": "live", "activity": "` + activity + `"},
		"observations": {},
		"created_at": "2026-09-20T10:00:00Z",
		"updated_at": "2026-09-20T10:00:00Z",
		"presence_changed_at": "2026-09-20T10:00:00Z",
		"activity_changed_at": "2026-09-20T10:00:00Z",
		"location": {"kind": "tmux", "server_id": "` + pane.ServerSocket + `", "session_id": "` + pane.SessionID + `", "session_name": "` + pane.SessionName + `", "window_id": "` + pane.WindowID + `", "pane_id": "` + pane.PaneID + `", "pane_pid": ` + strconv.Itoa(pane.PanePID) + `}
	}`
}

func activeWindow(t *testing.T, ctx context.Context, server *gotmux.Server, sessionID string) string {
	t.Helper()

	liveSession, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	_, links, err := liveSession.WindowInfos(ctx)
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

func runAgentNavigation(t *testing.T, action string, args ...string) agentNavResultJSON {
	t.Helper()

	stdout, stderr, err := executeCommand(t, append([]string{"agent", action}, args...)...)
	if err != nil {
		t.Fatalf("%s failed: %v\nstderr: %s", action, err, stderr)
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err != nil {
		t.Fatalf("decoding %s envelope %q: %v", action, stdout, err)
	}

	return decodeJSONPayload[agentNavResultJSON](t, envelope.Result)
}

func TestCLIAgentNextActivatesAgentWindow(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	historyDir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	historyPath := filepath.Join(historyDir, "history.json")

	result := runAgentNavigation(t, "next",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--agent-state", fixture.statePath,
		"--pane", fixture.current.PaneID, "--history", historyPath, testJSONFlag)

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
		t.Fatalf("loading state after agent next: %v", err)
	}

	ident := session.Identity{Origin: "local", Socket: fixture.target.ServerSocket, Name: fixture.target.SessionName}
	if got := st.GetLastAgentView(ident, fixture.target.PaneID); got == 0 {
		t.Fatal("agent next did not record agent view in history")
	}

	result = runAgentNavigation(t, "back",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", fixture.target.PaneID, testJSONFlag)

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

	_, stderr, err := executeCommand(t, "agent", "focus",
		testNoConfigFlag, testSocketFlag, fixture.socket, "--agent-state", fixture.statePath,
		"--pane", fixture.current.PaneID, fixture.target.PaneID)
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

func TestAgentNavigationActionsSucceedWithoutTerminalOutput(t *testing.T) {
	for _, action := range []string{"next", "next-local", "latest", "waiting", "failed", "unread", "toggle", "back", "focus"} {
		t.Run(action, func(t *testing.T) {
			fixture := newAgentIntegrationFixture(t)
			args, want := quietAgentNavigation(t, fixture, action)

			stdout, stderr, err := executeCommand(t, args...)
			if err != nil {
				t.Fatalf("navigation failed: %v\nstderr: %s", err, stderr)
			}

			if stdout != "" || stderr != "" {
				t.Fatalf("navigation output = %q / %q, want empty stdout and stderr", stdout, stderr)
			}

			if got := activeWindow(t, t.Context(), fixture.server, want.SessionID); got != want.WindowID {
				t.Fatalf("active window = %q, want %q", got, want.WindowID)
			}

			if got := livePane(t, t.Context(), fixture.server, want.PaneID); !got.PaneActive {
				t.Fatalf("pane %s is not active after navigation", want.PaneID)
			}
		})
	}
}

func quietAgentNavigation(t *testing.T, fixture agentIntegrationFixture, action string) ([]string, agents.PaneLocation) {
	t.Helper()

	historyPath := privateAgentHistoryPath(t)
	paneID := fixture.current.PaneID
	want := fixture.target
	command := action

	var extra []string

	switch action {
	case "latest", "unread":
		fixture.statePath = writeAgentRegistry(t, agentRegistrySessionForPane(fixture.target, "idle"))
	case "failed":
		fixture.statePath = writeAgentRegistry(t, agentRegistrySessionForPane(fixture.target, "failed"))
	case "toggle":
		runAgentNavigation(t, "focus", testNoConfigFlag, testSocketFlag, fixture.socket,
			"--agent-state", fixture.statePath, "--pane", paneID, fixture.target.PaneID,
			"--history", historyPath, testJSONFlag)
		paneID = fixture.target.PaneID
		want = fixture.current
	case "next-local":
		command = "next"
		extra = []string{"--local"}
	case "back":
		runAgentNavigation(t, "next", testNoConfigFlag, testSocketFlag, fixture.socket,
			"--agent-state", fixture.statePath, "--pane", paneID, "--history", historyPath, testJSONFlag)
		paneID = fixture.target.PaneID
		want = fixture.current
	case "focus":
		extra = []string{fixture.target.PaneID}
	}

	args := make([]string, 0, 11+len(extra))
	args = append(args,
		"agent", command, testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", paneID, "--history", historyPath,
	)

	return append(args, extra...), want
}

func TestCLIAgentAttentionRanksActivityTransitions(t *testing.T) {
	for _, tc := range []struct {
		action   string
		activity string
		current  string
		target   string
	}{
		{action: "waiting", activity: "waiting", current: "2026-09-20T12:00:00Z", target: "2026-09-20T09:00:00Z"},
		{action: "failed", activity: "failed", current: "2026-09-20T09:00:00Z", target: "2026-09-20T12:00:00Z"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			fixture := newAgentIntegrationFixture(t)
			fixture.statePath = writeAgentRegistry(t,
				strings.ReplaceAll(agentRegistrySessionForPane(fixture.current, tc.activity), "2026-09-20T10:00:00Z", tc.current),
				strings.ReplaceAll(agentRegistrySessionForPane(fixture.target, tc.activity), "2026-09-20T10:00:00Z", tc.target))
			historyPath := privateAgentHistoryPath(t)

			result := runAgentNavigation(t, tc.action, testNoConfigFlag, testSocketFlag, fixture.socket,
				"--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
				"--history", historyPath, "--local", testJSONFlag)
			if result.TargetPaneID != fixture.target.PaneID {
				t.Fatalf("target = %q, want %q", result.TargetPaneID, fixture.target.PaneID)
			}

			if got := activeWindow(t, t.Context(), fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
				t.Fatalf("active window = %q, want %q", got, fixture.target.WindowID)
			}
		})
	}
}

func TestCLIAgentUnreadAdvancesAndAcknowledgesObservedOrigin(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	older := newAgentWindow(t, fixture, "older-result")
	fixture.statePath = writeAgentRegistry(t,
		agentRegistrySessionForPane(fixture.current, "idle"),
		strings.ReplaceAll(agentRegistrySessionForPane(older, "idle"), "2026-09-20T10:00:00Z", "2026-09-20T11:00:00Z"),
		strings.ReplaceAll(agentRegistrySessionForPane(fixture.target, "idle"), "2026-09-20T10:00:00Z", "2026-09-20T12:00:00Z"))
	historyPath := privateAgentHistoryPath(t)

	current := fixture.current
	for _, want := range []agents.PaneLocation{fixture.target, older} {
		result := runAgentNavigation(t, "unread", testNoConfigFlag, testSocketFlag, fixture.socket,
			"--agent-state", fixture.statePath, "--pane", current.PaneID, "--history", historyPath, testJSONFlag)
		if result.TargetPaneID != want.PaneID {
			t.Fatalf("unread target = %q, want %q", result.TargetPaneID, want.PaneID)
		}

		if got := activeWindow(t, t.Context(), fixture.server, want.SessionID); got != want.WindowID {
			t.Fatalf("active window = %q, want %q", got, want.WindowID)
		}

		current = want
	}

	expectAgentNoTarget(t, "unread", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", older.PaneID, "--history", historyPath, testJSONFlag)

	if got := activeWindow(t, t.Context(), fixture.server, older.SessionID); got != older.WindowID {
		t.Fatalf("exhausted unread changed active window to %q", got)
	}

	registry := `{"schema_version":3,"revision":2,"sessions":{` +
		agentRegistrySessionForPane(fixture.current, "idle") + "," +
		strings.ReplaceAll(agentRegistrySessionForPane(older, "idle"), "2026-09-20T10:00:00Z", "2026-09-20T11:00:00Z") + "," +
		strings.ReplaceAll(agentRegistrySessionForPane(fixture.target, "idle"), "2026-09-20T10:00:00Z", "2027-09-20T12:00:00Z") + `}}`
	if err := os.WriteFile(fixture.statePath, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runAgentNavigation(t, "unread", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", older.PaneID, "--history", historyPath, testJSONFlag)
	if result.TargetPaneID != fixture.target.PaneID {
		t.Fatalf("renewed idle transition = %q, want %q", result.TargetPaneID, fixture.target.PaneID)
	}

	if got := activeWindow(t, t.Context(), fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
		t.Fatalf("renewed result active window = %q, want %q", got, fixture.target.WindowID)
	}
}

func newAgentWindow(t *testing.T, fixture agentIntegrationFixture, name string) agents.PaneLocation {
	t.Helper()

	liveSession, err := fixture.server.Session(t.Context(), gotmux.SessionID(fixture.current.SessionID))
	if err != nil {
		t.Fatal(err)
	}

	window, err := liveSession.NewWindow(t.Context(), gotmux.NewWindowOptions{
		Name: name, Dir: "", Program: gotmux.Exec("sleep", "300"), Env: nil, Select: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	pane, err := window.Window().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return livePane(t, t.Context(), fixture.server, string(pane.ID()))
}

func privateAgentHistoryPath(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, "history.json")
}

func TestCLIAgentToggleRepeatedlyReturnsToWorkingAndAgentPanes(t *testing.T) {
	for _, action := range []string{"focus", "latest"} {
		t.Run(action, func(t *testing.T) {
			fixture := newAgentIntegrationFixture(t)
			fixture.statePath = writeAgentRegistry(t, agentRegistrySessionForPane(fixture.target, "idle"))
			historyPath := privateAgentHistoryPath(t)

			args := []string{
				testNoConfigFlag, testSocketFlag, fixture.socket, "--agent-state", fixture.statePath,
				"--pane", fixture.current.PaneID, "--history", historyPath, testJSONFlag,
			}
			if action == "focus" {
				args = append(args, fixture.target.PaneID)
			}

			runAgentNavigation(t, action, args...)

			current := fixture.target
			for _, want := range []agents.PaneLocation{fixture.current, fixture.target, fixture.current, fixture.target} {
				result := runAgentNavigation(t, "toggle", testNoConfigFlag, testSocketFlag, fixture.socket,
					"--agent-state", fixture.statePath, "--pane", current.PaneID, "--history", historyPath, "--local", testJSONFlag)
				if result.TargetPaneID != want.PaneID {
					t.Fatalf("toggle target = %q, want %q", result.TargetPaneID, want.PaneID)
				}

				if got := activeWindow(t, t.Context(), fixture.server, want.SessionID); got != want.WindowID {
					t.Fatalf("active window = %q, want %q", got, want.WindowID)
				}

				current = want
			}

			back := runAgentNavigation(t, "back", testNoConfigFlag, testSocketFlag, fixture.socket,
				"--agent-state", fixture.statePath, "--pane", current.PaneID, testJSONFlag)
			if back.TargetPaneID != fixture.current.PaneID {
				t.Fatalf("back after toggle = %q, want %q", back.TargetPaneID, fixture.current.PaneID)
			}
		})
	}
}

func TestCLIAgentToggleWithoutRememberedAgentPreservesActiveWindow(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	expectAgentNoTarget(t, "toggle", testNoConfigFlag,
		testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", fixture.current.PaneID, testJSONFlag)

	if got := activeWindow(t, t.Context(), fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("toggle without history changed active window to %q", got)
	}
}

func TestCLIAgentToggleKeepsEachClientsLastAgent(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	other := newAgentWindow(t, fixture, "other-agent")
	fixture.statePath = writeAgentRegistry(t,
		agentRegistrySessionForPane(fixture.target, "idle"), agentRegistrySessionForPane(other, "idle"))
	historyPath := privateAgentHistoryPath(t)
	clients := cliSessionFixture{
		server: fixture.server, socket: fixture.socket, historyPath: historyPath,
		repository: "", leaseRoot: "", dataHome: "",
	}
	first := clients.attachCLIClient(t, gotmux.SessionID(fixture.current.SessionID))

	second := clients.attachCLIClient(t, gotmux.SessionID(fixture.current.SessionID))
	for _, tc := range []struct {
		client string
		target agents.PaneLocation
	}{{client: first, target: fixture.target}, {client: second, target: other}} {
		runAgentNavigation(t, "focus", testNoConfigFlag, testSocketFlag, fixture.socket,
			"--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
			"--client", tc.client, tc.target.PaneID, "--history", historyPath, testJSONFlag)
	}

	for _, tc := range []struct {
		client string
		target agents.PaneLocation
	}{{client: first, target: fixture.target}, {client: second, target: other}} {
		result := runAgentNavigation(t, "toggle", testNoConfigFlag, testSocketFlag, fixture.socket,
			"--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
			"--client", tc.client, "--history", historyPath, testJSONFlag)
		if result.TargetPaneID != tc.target.PaneID {
			t.Fatalf("client toggle = %q, want %q", result.TargetPaneID, tc.target.PaneID)
		}

		if got := activeWindow(t, t.Context(), fixture.server, tc.target.SessionID); got != tc.target.WindowID {
			t.Fatalf("client toggle active window = %q, want %q", got, tc.target.WindowID)
		}
	}
}

func TestCLIAgentFocusRejectsInvalidExplicitClientBeforeMovingPane(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	stdout, stderr, err := executeCommand(t, "agent", "focus", testNoConfigFlag,
		testSocketFlag, fixture.socket, "--agent-state", fixture.statePath,
		"--pane", fixture.current.PaneID, "--client", "/dev/invalid-client",
		fixture.target.PaneID, "--history", privateAgentHistoryPath(t), testJSONFlag)
	if err == nil || stdout != "" {
		t.Fatalf("invalid client focus = %q, %q, %v", stdout, stderr, err)
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.OK || envelope.Error == nil {
		t.Fatalf("invalid client envelope = %+v", envelope)
	}

	if got := activeWindow(t, t.Context(), fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("invalid client focus changed active window to %q", got)
	}

	result := runAgentNavigation(t, "focus", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
		fixture.target.PaneID, "--history", privateAgentHistoryPath(t), testJSONFlag)
	if result.TargetPaneID != fixture.target.PaneID {
		t.Fatalf("valid focus = %q, want %q", result.TargetPaneID, fixture.target.PaneID)
	}

	if got := activeWindow(t, t.Context(), fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
		t.Fatalf("valid focus active window = %q, want %q", got, fixture.target.WindowID)
	}
}

func TestCLIAgentToggleRejectsVanishedAgentAndKeepsWorkingPane(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	historyPath := privateAgentHistoryPath(t)
	runAgentNavigation(t, "focus", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
		fixture.target.PaneID, "--history", historyPath, testJSONFlag)
	runAgentNavigation(t, "toggle", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", fixture.target.PaneID, "--history", historyPath, testJSONFlag)

	window, err := fixture.server.Window(t.Context(), gotmux.WindowID(fixture.target.WindowID))
	if err != nil {
		t.Fatal(err)
	}

	if err := window.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	expectAgentNoTarget(t, "toggle", testNoConfigFlag,
		testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", fixture.current.PaneID,
		"--history", historyPath, testJSONFlag)

	if got := activeWindow(t, t.Context(), fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("vanished agent changed active window to %q", got)
	}
}

func expectAgentNoTarget(t *testing.T, action string, args ...string) {
	t.Helper()

	stdout, stderr, err := executeCommand(t, append([]string{"agent", action}, args...)...)
	if err == nil || stdout != "" {
		t.Fatalf("%s without target = %q, %q, %v", action, stdout, stderr, err)
	}

	var envelope testJSONEnvelope
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "notFound" {
		t.Fatalf("%s missing target error = %+v", action, envelope)
	}
}

func TestCLIAgentStatusReadsRegistry(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)

	stdout, stderr, err := executeCommand(t, "agent", "status", testNoConfigFlag, "--agent-state", fixture.statePath)
	if err != nil {
		t.Fatalf("agent status failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Agents: 1 live") || !strings.Contains(stdout, "waiting: 1") {
		t.Fatalf("unexpected status output: %q", stdout)
	}
}

func TestCLIAgentLatestActivatesNewestIdleAndReturnsThroughHistory(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	fixture.statePath = writeAgentRegistry(t,
		agentRegistrySessionForPane(fixture.current, "idle"),
		strings.ReplaceAll(agentRegistrySessionForPane(fixture.target, "idle"), "2026-09-20T10:00:00Z", "2026-09-20T11:00:00Z"))

	historyPath := privateAgentHistoryPath(t)
	for _, pane := range []agents.PaneLocation{fixture.current, fixture.target} {
		stdout, stderr, err := executeCommand(t, "agent", "latest", testNoConfigFlag,
			testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", pane.PaneID,
			"--history", historyPath, "--local", testJSONFlag)
		if err != nil || stderr != "" {
			t.Fatalf("latest failed: %v, stderr = %q", err, stderr)
		}

		var envelope testJSONEnvelope
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}

		result := decodeJSONPayload[agentNavResultJSON](t, envelope.Result)
		if result.TargetPaneID != fixture.target.PaneID {
			t.Fatalf("target = %q", result.TargetPaneID)
		}

		if got := activeWindow(t, t.Context(), fixture.server, fixture.target.SessionID); got != fixture.target.WindowID {
			t.Fatalf("active window = %q", got)
		}

		if !livePane(t, t.Context(), fixture.server, fixture.target.PaneID).PaneActive {
			t.Fatal("target pane is inactive")
		}
	}

	result := runAgentNavigation(t, "back", testNoConfigFlag, testSocketFlag, fixture.socket,
		"--agent-state", fixture.statePath, "--pane", fixture.target.PaneID, testJSONFlag)
	if result.TargetPaneID != fixture.current.PaneID {
		t.Fatalf("back target = %q", result.TargetPaneID)
	}

	if got := activeWindow(t, t.Context(), fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("active window after back = %q", got)
	}

	if !livePane(t, t.Context(), fixture.server, fixture.current.PaneID).PaneActive {
		t.Fatal("origin pane is inactive")
	}
}

func TestCLIAgentLatestNoTargetPreservesActiveWindow(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	logPath := filepath.Join(t.TempDir(), "private-log.jsonl")
	t.Setenv("SESH_PATHS_LOG_FILE", logPath)
	t.Setenv("SESH_SETTINGS_LOG_LEVEL", "info")

	stdout, stderr, err := executeCommand(t, "agent", "latest", testNoConfigFlag,
		testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", fixture.current.PaneID, testJSONFlag)
	if err == nil {
		t.Fatal("waiting agent was accepted as idle")
	}

	var envelope testJSONEnvelope

	if stdout != "" {
		t.Fatalf("failure wrote stdout = %q", stdout)
	}

	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "notFound" {
		t.Fatalf("envelope = %+v", envelope)
	}

	assertAgentLatestFailureLog(t, logPath, fixture.socket, fixture.statePath, fixture.current.PaneID, fixture.target.PaneID)

	if got := activeWindow(t, t.Context(), fixture.server, fixture.current.SessionID); got != fixture.current.WindowID {
		t.Fatalf("active window changed to %q", got)
	}
}

func assertAgentLatestFailureLog(t *testing.T, logPath string, privateValues ...string) {
	t.Helper()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("records = %s", data)
	}

	expected := []map[string]any{
		{"level": "info", "component": "cli", "operation": "latest", "status": "started"},
		{"level": "error", "component": "cli", "operation": "latest", "status": "failed", "reason": "notFound"},
	}
	for index, fields := range expected {
		var record map[string]any
		if err := json.Unmarshal([]byte(lines[index]), &record); err != nil {
			t.Fatal(err)
		}

		for key, want := range fields {
			if record[key] != want {
				t.Fatalf("record[%s] = %v, want %v", key, record[key], want)
			}
		}
	}

	for _, private := range privateValues {
		if strings.Contains(string(data), private) {
			t.Fatalf("private value in log = %s", data)
		}
	}
}
