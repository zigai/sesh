//go:build (linux || darwin) && integration

package agents_test

import (
	"context"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/agents"
)

func attachTestClient(t *testing.T, ctx context.Context, server *gotmux.Server, session gotmux.Session) string {
	t.Helper()

	master, slave, err := openTestPTY(t)
	if err != nil {
		t.Skipf("pty allocation unavailable: %v", err)
	}

	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})

	sessionID := session.ID()

	attachCtx, cancelAttach := context.WithCancel(context.WithoutCancel(ctx))

	done := make(chan error, 1)

	go func() {
		done <- session.Attach(attachCtx, gotmux.Streams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{
			ReadOnly:            true,
			PreserveEnvironment: true,
			Detach:              gotmux.DetachModeNone,
			Dir:                 "",
			Flags:               nil,
		})
	}()

	t.Cleanup(func() {
		cancelAttach()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Logf("attach client for %s did not exit in time", sessionID)
		}
	})

	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		clients, clientErr := server.Clients(ctx)
		if clientErr != nil {
			t.Fatalf("listing clients: %v", clientErr)
		}

		for _, client := range clients {
			if client.Control {
				continue
			}

			attachedSession, ok := client.SessionID.Get()
			if ok && attachedSession == sessionID {
				return string(client.Name)
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("attached client for session %s never appeared", sessionID)

	return ""
}

func clientSession(t *testing.T, ctx context.Context, server *gotmux.Server, clientName string) string {
	t.Helper()

	clients, err := server.Clients(ctx)
	if err != nil {
		t.Fatalf("listing clients: %v", err)
	}

	for _, client := range clients {
		if string(client.Name) != clientName {
			continue
		}

		sessionID, ok := client.SessionID.Get()
		if !ok {
			return ""
		}

		return string(sessionID)
	}

	t.Fatalf("client %s disappeared", clientName)

	return ""
}

func activeWindowID(t *testing.T, ctx context.Context, server *gotmux.Server, sessionID string) string {
	t.Helper()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	infos, linkInfos, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("reading session windows: %v", err)
	}

	for _, link := range linkInfos {
		if link.Active {
			return string(link.WindowID)
		}
	}

	if len(infos) == 0 {
		t.Fatalf("session %s has no windows", sessionID)
	}

	return ""
}

func paneActive(t *testing.T, ctx context.Context, server *gotmux.Server, paneID string) bool {
	t.Helper()

	panes, err := agents.ObservePanes(ctx, server)
	if err != nil {
		t.Fatalf("observing panes: %v", err)
	}

	for _, pane := range panes {
		if pane.PaneID == paneID {
			return pane.PaneActive && pane.WindowActive
		}
	}

	t.Fatalf("pane %s is not a live pane", paneID)

	return false
}

func windowActive(t *testing.T, ctx context.Context, server *gotmux.Server, sessionID string, windowID string) bool {
	t.Helper()

	return activeWindowID(t, ctx, server, sessionID) == windowID
}

func livePaneFor(t *testing.T, ctx context.Context, server *gotmux.Server, paneID string) agents.PaneLocation {
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

	t.Fatalf("pane %s is not a live pane", paneID)

	return agents.PaneLocation{}
}

func paneFixture(pane agents.PaneLocation, activity string) registryFixture {
	return registryFixture{
		ID:           "agent-" + pane.PaneID,
		Harness:      "omp",
		Activity:     activity,
		ServerSocket: pane.ServerSocket,
		SessionID:    pane.SessionID,
		SessionName:  pane.SessionName,
		WindowID:     pane.WindowID,
		PaneID:       pane.PaneID,
		PanePID:      pane.PanePID,
	}
}

func newTmuxFixture(t *testing.T) (*gotmux.Server, string, agents.PaneLocation) {
	t.Helper()

	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("fixture sessions = %d, err = %v", len(sessions), err)
	}

	windows, _, err := sessions[0].Handle().WindowInfos(ctx)
	if err != nil || len(windows) == 0 {
		t.Fatalf("fixture windows: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("fixture active pane: %v", err)
	}

	return server, string(sessions[0].ID), livePaneFor(t, ctx, server, string(pane.ID()))
}
