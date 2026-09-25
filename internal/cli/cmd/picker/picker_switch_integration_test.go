//go:build linux && integration

package cmdpicker

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

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestPickerAgentSelectionSwitchesClientAndFocusesPane(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}
	origin := sessions[0]
	originPanes, err := origin.Handle().Panes(ctx)
	if err != nil || len(originPanes) == 0 {
		t.Fatalf("origin panes: %v", err)
	}

	targetSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "agent-target"})
	if err != nil {
		t.Fatal(err)
	}
	agentWindow, err := targetSession.NewWindow(ctx, gotmux.NewWindowOptions{Name: "agent", Program: gotmux.Exec("sleep", "300"), Select: false})
	if err != nil {
		t.Fatal(err)
	}
	agentPane, err := agentWindow.Window().ActivePane(ctx)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := server.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", server.Endpoint().SocketPath+","+strconv.Itoa(probe.Identity.PID)+","+strings.TrimPrefix(string(origin.ID), "$"))
	t.Setenv("TMUX_PANE", string(originPanes[0].ID))

	clientName := attachPickerTestClient(t, origin.Handle())
	root := t.TempDir()
	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	services, err := newPickerServices(ctx, config.DefaultConfig(), server.Endpoint().SocketPath,
		filepath.Join(root, "repository"), filepath.Join(root, "leases"), filepath.Join(historyDir, "history.json"),
		filepath.Join(root, "agents.json"), gotmux.TerminalStreams{}, PickerRemoteServices{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Close() })

	rows, err := session.ListLive(ctx, server, string(originPanes[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	var target session.Session
	for _, row := range rows {
		if row.ID == string(targetSession.ID()) {
			target = row
			break
		}
	}
	if target.ID == "" {
		t.Fatal("target session missing from live catalog")
	}

	if err := services.performDeferredSwitch(ctx, target, string(agentPane.ID())); err != nil {
		t.Fatalf("agent selection failed: %v", err)
	}
	client, err := server.Client(ctx, gotmux.ClientName(clientName))
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if displayed, ok := info.SessionID.Get(); !ok || displayed != targetSession.ID() {
		t.Fatalf("client displays %q, want %q", displayed, targetSession.ID())
	}
	links, err := targetSession.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	focused := false
	for _, link := range links {
		window, err := link.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if window.Active {
			focused = window.WindowID == agentWindow.Window().ID()
			if !focused {
				t.Fatalf("active window = %q, want agent window %q", window.WindowID, agentWindow.Window().ID())
			}
		}
	}
	if !focused {
		t.Fatal("target session has no active agent window")
	}

	st, err := session.LoadState(filepath.Join(historyDir, "history.json"))
	if err != nil {
		t.Fatalf("loading history after switch: %v", err)
	}
	if got := st.GetLastAgentView(target.SemanticIdentity(), string(agentPane.ID())); got == 0 {
		t.Fatal("deferred switch did not record agent view in history")
	}
}

func TestPopupHandoffSwitchesOnlyOpeningClientAfterPopupSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()
	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}
	origin := sessions[0]
	targetSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "mobile-target", Program: gotmux.Exec("sleep", "300")})
	if err != nil {
		t.Fatal(err)
	}
	openingClient := attachPickerTestClient(t, origin.Handle())
	otherClient := attachPickerTestClient(t, origin.Handle())

	root := t.TempDir()
	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	target, err := findPickerLiveTarget(ctx, server, string(targetSession.ID()), "mobile-target")
	if err != nil {
		t.Fatal(err)
	}
	if err := writePickerSwitchHandoff(path, target, ""); err != nil {
		t.Fatal(err)
	}
	runtime := pickerCommandRuntime{
		cfg: config.DefaultConfig(), socketPath: server.Endpoint().SocketPath,
		repositoryPath: filepath.Join(root, "repository"), leaseRoot: filepath.Join(root, "leases"),
		historyPath: filepath.Join(historyDir, "history.json"),
	}
	services, err := newPickerServices(ctx, runtime.cfg, runtime.socketPath, runtime.repositoryPath,
		runtime.leaseRoot, runtime.historyPath, "", gotmux.TerminalStreams{In: nil, Out: nil, Err: nil},
		PickerRemoteServices{Catalog: nil, Watcher: nil, Actions: nil, Attach: nil, OriginBridge: nil})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Close() })
	if err := completePickerSwitchHandoff(ctx, services, path, openingClient, string(origin.ID)); err != nil {
		t.Fatalf("popup handoff failed: %v", err)
	}
	clients, err := server.Clients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want gotmux.SessionID
	}{{openingClient, targetSession.ID()}, {otherClient, origin.ID}} {
		found := false
		for _, client := range clients {
			if string(client.Name) != test.name {
				continue
			}
			found = true
			if displayed, ok := client.SessionID.Get(); !ok || displayed != test.want {
				t.Fatalf("client %q displays %q, want %q", test.name, displayed, test.want)
			}
		}
		if !found {
			t.Fatalf("client %q disappeared", test.name)
		}
	}
}

func TestResolvePopupClientSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	t.Setenv(pickerClientEnvironment, "")

	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}
	origin := sessions[0]

	conn, err := server.OpenControl(ctx, origin.Handle(), gotmux.ControlOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cmd := NewPopupCommand(ctx)
	if _, err := resolvePopupClient(ctx, cmd, server); !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("expected ErrInvokingClientUnavailable for lone control client, got: %v", err)
	}

	openingClient := attachPickerTestClient(t, origin.Handle())

	cmdAmbient := NewPopupCommand(ctx)
	resolvedAmbient, err := resolvePopupClient(ctx, cmdAmbient, server)
	if err != nil || string(resolvedAmbient.Name) != openingClient {
		t.Fatalf("resolvePopupClient ambient = %v, want %q", err, openingClient)
	}

	cmdExplicit := NewPopupCommand(ctx)
	_ = cmdExplicit.Flags().Set("client", openingClient)
	resolved, err := resolvePopupClient(ctx, cmdExplicit, server)
	if err != nil || string(resolved.Name) != openingClient {
		t.Fatalf("resolvePopupClient explicit = %v, want %q", err, openingClient)
	}

	clients, err := server.Clients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if c.Control {
			cmdControl := NewPopupCommand(ctx)
			_ = cmdControl.Flags().Set("client", string(c.Name))
			if _, err := resolvePopupClient(ctx, cmdControl, server); !errors.Is(err, session.ErrInvokingClientUnavailable) {
				t.Fatalf("expected ErrInvokingClientUnavailable for explicit control client, got: %v", err)
			}
			break
		}
	}

	cmdUnknown := NewPopupCommand(ctx)
	_ = cmdUnknown.Flags().Set("client", "/dev/pts/nonexistent-client-id")
	if _, err := resolvePopupClient(ctx, cmdUnknown, server); !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("expected ErrInvokingClientUnavailable for unknown explicit client, got: %v", err)
	}
}

func attachPickerTestClient(t testing.TB, session gotmux.Session) string {
	t.Helper()
	before, err := session.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	existing := make(map[gotmux.ClientName]bool, len(before))
	for _, client := range before {
		existing[client.Name] = true
	}
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

	attachCtx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)
	go func() {
		done <- session.Attach(attachCtx, gotmux.TerminalStreams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("attached client did not stop")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		clients, err := session.Clients(t.Context())
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
