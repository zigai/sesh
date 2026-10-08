//go:build linux && integration

package picker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

func TestPickerAgentSelectionSwitchesClientAndFocusesPane(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	origin := pickerOriginSession(t, server)

	originPanes, err := origin.Handle().Panes(ctx)
	if err != nil || len(originPanes) == 0 {
		t.Fatalf("origin panes: %v", err)
	}

	targetSession, agentWindow, agentPane := newPickerAgentTarget(t, server)

	probe, err := server.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMUX", server.Endpoint().SocketPath+","+strconv.Itoa(probe.Identity.PID)+","+strings.TrimPrefix(string(origin.ID), "$"))
	t.Setenv("TMUX_PANE", string(originPanes[0].ID))

	clientName := attachPickerTestClient(t, origin.Handle())
	root := t.TempDir()
	historyPath := pickerHistoryPath(t, root)

	services, err := newPickerServices(ctx, resolvePickerTestWorkspace(t, config.DefaultConfig(), app.Locations{
		Socket: server.Endpoint().SocketPath, Repository: filepath.Join(root, "repository"), LeaseRoot: filepath.Join(root, "leases"),
		History: historyPath, FinalSaveStatus: "", AgentStore: filepath.Join(root, "agents.json"),
	}), gotmux.Streams{}, RemoteServices{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = services.Close() })

	target, err := findPickerLiveTarget(ctx, server, string(targetSession.ID()), "agent-target")
	if err != nil {
		t.Fatalf("target session missing from live catalog: %v", err)
	}

	if err := services.performDeferredSwitch(ctx, target, string(agentPane)); err != nil {
		t.Fatalf("agent selection failed: %v", err)
	}

	assertPickerClientDisplays(t, server, clientName, targetSession.ID())
	assertPickerActiveWindow(t, targetSession, agentWindow)

	st, err := session.LoadState(historyPath)
	if err != nil {
		t.Fatalf("loading history after switch: %v", err)
	}

	if got := st.GetLastAgentView(target.SemanticIdentity(), string(agentPane)); got == 0 {
		t.Fatal("deferred switch did not record agent view in history")
	}
}

func pickerOriginSession(t *testing.T, server *gotmux.Server) gotmux.SessionInfo {
	t.Helper()

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}

	return sessions[0]
}

// newPickerAgentTarget creates a session whose unselected second window runs the agent pane.
func newPickerAgentTarget(t *testing.T, server *gotmux.Server) (gotmux.Session, gotmux.WindowID, gotmux.PaneID) {
	t.Helper()

	target, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "agent-target"})
	if err != nil {
		t.Fatal(err)
	}

	link, err := target.NewWindow(t.Context(), gotmux.NewWindowOptions{Name: "agent", Program: gotmux.Exec("sleep", "300"), Select: false})
	if err != nil {
		t.Fatal(err)
	}

	pane, err := link.Window().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return target, link.Window().ID(), pane.ID()
}

func pickerHistoryPath(t *testing.T, root string) string {
	t.Helper()

	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(historyDir, "history.json")
}

func TestPopupHandoffSwitchesOnlyOpeningClientAfterPopupSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	origin := pickerOriginSession(t, server)

	targetSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "mobile-target", Program: gotmux.Exec("sleep", "300")})
	if err != nil {
		t.Fatal(err)
	}

	openingClient := attachPickerTestClient(t, origin.Handle())
	otherClient := attachPickerTestClient(t, origin.Handle())

	root := t.TempDir()

	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	target, err := findPickerLiveTarget(ctx, server, string(targetSession.ID()), "mobile-target")
	if err != nil {
		t.Fatal(err)
	}

	originView := tui.AgentView{
		Session: session.Identity{Origin: "local", Socket: server.Endpoint().SocketPath, Name: "origin-agent-session"},
		PaneID:  "%0",
	}
	if err := writePickerSwitchHandoff(path, &target, "", "", []tui.AgentView{originView}); err != nil {
		t.Fatal(err)
	}

	runtime := pickerCommandRuntime{
		workspace: resolvePickerTestWorkspace(t, config.DefaultConfig(), app.Locations{
			Socket: server.Endpoint().SocketPath, Repository: filepath.Join(root, "repository"),
			LeaseRoot: filepath.Join(root, "leases"), History: pickerHistoryPath(t, root), FinalSaveStatus: "", AgentStore: "",
		}),
	}

	services, err := newPickerServices(ctx, runtime.workspace, gotmux.Streams{In: nil, Out: nil, Err: nil},
		RemoteServices{Catalog: nil, Watcher: nil, Actions: nil, Attach: nil, OriginBridge: nil})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = services.Close() })

	if err := completePickerSwitchHandoff(ctx, services, path, openingClient, string(origin.ID)); err != nil {
		t.Fatalf("popup handoff failed: %v", err)
	}

	assertPickerClientDisplays(t, server, openingClient, targetSession.ID())
	assertPickerClientDisplays(t, server, otherClient, origin.ID)

	// The invoking process records the switch and the popup's deferred views together.
	st, err := session.LoadState(runtime.workspace.History)
	if err != nil {
		t.Fatal(err)
	}

	switched := session.Identity{Origin: "local", Socket: server.Endpoint().SocketPath, Name: "mobile-target"}
	if st.GetLastSwitchIdentity(switched) == 0 {
		t.Fatal("popup handoff did not record the switch in history")
	}

	if st.GetLastAgentView(originView.Session, originView.PaneID) == 0 {
		t.Fatal("popup handoff did not record the popup's deferred agent view")
	}
}

func TestResolvePopupClientSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	t.Setenv(pickerClientEnvironment, "")

	origin := pickerOriginSession(t, server)

	conn, err := server.OpenControl(ctx, origin.Handle().ID(), gotmux.ControlOptions{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	assertPopupClientRejected(t, server, "", "lone control client")

	openingClient := attachPickerTestClient(t, origin.Handle())

	assertPopupClientResolves(t, server, "", openingClient)
	assertPopupClientResolves(t, server, openingClient, openingClient)
	assertPopupClientRejected(t, server, pickerControlClient(t, server), "explicit control client")
	assertPopupClientRejected(t, server, "/dev/pts/nonexistent-client-id", "unknown explicit client")
}

// resolvePopupClientFlag resolves the popup client, passing --client only when flag is set.
func resolvePopupClientFlag(t *testing.T, server *gotmux.Server, flag string) (gotmux.ClientInfo, error) {
	t.Helper()

	cmd := NewPickerOpenCommand(t.Context())
	if flag != "" {
		if err := cmd.Flags().Set("client", flag); err != nil {
			t.Fatal(err)
		}
	}

	return resolvePopupClient(t.Context(), cmd, server)
}

func assertPopupClientResolves(t *testing.T, server *gotmux.Server, flag, want string) {
	t.Helper()

	resolved, err := resolvePopupClientFlag(t, server, flag)
	if err != nil || string(resolved.Name) != want {
		t.Fatalf("resolvePopupClient(--client=%q) = %q, %v; want %q", flag, resolved.Name, err, want)
	}
}

func assertPopupClientRejected(t *testing.T, server *gotmux.Server, flag, description string) {
	t.Helper()

	if _, err := resolvePopupClientFlag(t, server, flag); !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("expected ErrInvokingClientUnavailable for %s, got: %v", description, err)
	}
}

func pickerControlClient(t *testing.T, server *gotmux.Server) string {
	t.Helper()

	clients, err := server.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, client := range clients {
		if client.Control {
			return string(client.Name)
		}
	}

	t.Fatal("control client missing")

	return ""
}

func assertPickerClientDisplays(t *testing.T, server *gotmux.Server, name string, want gotmux.SessionID) {
	t.Helper()

	client, err := server.Client(t.Context(), gotmux.ClientName(name))
	if err != nil {
		t.Fatalf("client %q disappeared: %v", name, err)
	}

	info, err := client.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if displayed, ok := info.SessionID.Get(); !ok || displayed != want {
		t.Fatalf("client %q displays %q, want %q", name, displayed, want)
	}
}

func assertPickerActiveWindow(t *testing.T, target gotmux.Session, want gotmux.WindowID) {
	t.Helper()

	links, err := target.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, link := range links {
		window, err := link.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if window.Active {
			if window.WindowID != want {
				t.Fatalf("active window = %q, want %q", window.WindowID, want)
			}

			return
		}
	}

	t.Fatal("target session has no active window")
}

func attachPickerTestClient(t *testing.T, liveSession gotmux.Session) string {
	t.Helper()

	before, err := liveSession.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	existing := make(map[gotmux.ClientName]bool, len(before))
	for _, client := range before {
		existing[client.Name] = true
	}

	slave := openPickerTestPTY(t)

	attachCtx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))

	done := make(chan error, 1)
	go func() {
		done <- liveSession.Attach(attachCtx, gotmux.Streams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{})
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("attached client did not stop")
		}
	})

	return waitForNewPickerClient(t, liveSession, existing)
}

// openPickerTestPTY allocates a pseudo-terminal and returns its slave side for an attached client.
func openPickerTestPTY(t *testing.T) *os.File {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = master.Close() })

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}

	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = slave.Close() })

	return slave
}

func waitForNewPickerClient(t *testing.T, liveSession gotmux.Session, existing map[gotmux.ClientName]bool) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		clients, err := liveSession.Clients(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		for _, client := range clients {
			if !client.Control && !existing[client.Name] {
				return string(client.Name)
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("attached client did not appear")

	return ""
}
