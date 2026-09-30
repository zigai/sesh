//go:build (linux || darwin) && integration

package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

func TestAgentPositionRefreshTracksLivePaneSwapsAndMoves(t *testing.T) {
	server := tmuxtest.NewServer(t)
	source := newPositionSession(t, server, "sort")
	left := positionSessionPane(t, source)

	right, err := left.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionHorizontal})
	if err != nil {
		t.Fatal(err)
	}

	bottom, err := left.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionVertical})
	if err != nil {
		t.Fatal(err)
	}

	windowTen := newPositionWindowPane(t, source, 10)
	windowTwo := newPositionWindowPane(t, source, 2)
	model := newLivePositionModel(t, server, source, []gotmux.Pane{windowTen, right, windowTwo, bottom, left})

	if err := model.loadSessions(); err != nil {
		t.Fatal(err)
	}

	model.cursor = assertLivePositionOrder(t, model, source, left, bottom, right, windowTwo, windowTen)
	model.agentCursor = 0
	selected := model.selectedSessionIdentity()

	if selected.agentID != string(left.ID()) {
		t.Fatalf("selected agent = %q, want %q", selected.agentID, left.ID())
	}

	if err := left.Swap(t.Context(), right, gotmux.SwapPaneOptions{Select: false}); err != nil {
		t.Fatal(err)
	}

	refresh := model.loadAgentPositionsCommand()
	if refresh == nil {
		t.Fatal("position refresh did not produce a command")
	}

	updateTestModel(t, model, refresh())
	assertLivePositionOrder(t, model, source, right, bottom, left, windowTwo, windowTen)
	assertPositionSelectionVisible(t, model, selected)

	foreign := newPositionSession(t, server, "other")
	if err := bottom.Join(t.Context(), positionSessionPane(t, foreign), gotmux.JoinOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := model.loadSessions(); err != nil {
		t.Fatal(err)
	}

	assertLivePositionOrder(t, model, source, right, left, windowTwo, windowTen)
	assertPositionSelectionVisible(t, model, selected)
}

func newPositionSession(t *testing.T, server *gotmux.Server, name string) gotmux.Session {
	t.Helper()

	result, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: name,
		Size: gotmux.Size{Width: 120, Height: 30},
	})
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func positionSessionPane(t *testing.T, owner gotmux.Session) gotmux.Pane {
	t.Helper()

	panes, err := owner.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(panes) != 1 {
		t.Fatalf("new session has %d panes, want 1", len(panes))
	}

	return panes[0].Handle()
}

func newPositionWindowPane(t *testing.T, owner gotmux.Session, index int) gotmux.Pane {
	t.Helper()

	window, err := owner.NewWindow(t.Context(), gotmux.NewWindowOptions{Index: &index})
	if err != nil {
		t.Fatal(err)
	}

	panes, err := window.Window().Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(panes) != 1 {
		t.Fatalf("new window has %d panes, want 1", len(panes))
	}

	return panes[0].Handle()
}

func newLivePositionModel(t *testing.T, server *gotmux.Server, owner gotmux.Session, order []gotmux.Pane) *Model {
	t.Helper()

	registry := make([]session.AgentSession, 0, len(order))
	for _, pane := range order {
		info, err := pane.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		registry = append(registry, session.AgentSession{
			RegistryID: string(pane.ID()), PaneID: string(pane.ID()),
			HarnessSession: string(pane.ID()), PanePID: info.PID,
			Status: session.AgentStatusRunning,
		})
	}

	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(ctx context.Context) (session.CatalogResult, error) {
			return session.ListCatalog(ctx, session.CatalogOptions{Server: server})
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{{
				TmuxServerSocket: server.Endpoint().SocketPath,
				TmuxSessionID:    string(owner.ID()), TmuxSessionName: "sort",
				Agents: slices.Clone(registry), Total: len(registry), Active: len(registry),
			}}, nil
		},
		TmuxPaneLocations: func(ctx context.Context) ([]PaneLocation, error) {
			return observePositionPanes(ctx, server)
		},
	})
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents

	return model
}

func observePositionPanes(ctx context.Context, server *gotmux.Server) ([]PaneLocation, error) {
	panes, err := agents.ObservePanes(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("observe picker pane positions: %w", err)
	}

	result := make([]PaneLocation, 0, len(panes))
	for _, pane := range panes {
		result = append(result, PaneLocation{
			ServerSocket: pane.ServerSocket, SessionID: pane.SessionID, SessionName: pane.SessionName,
			WindowID: pane.WindowID, WindowIndex: pane.WindowIndex,
			PaneID: pane.PaneID, PaneIndex: pane.PaneIndex, PanePID: pane.PanePID, Command: pane.Command,
			PaneLeft: strconv.Itoa(pane.Left), PaneTop: strconv.Itoa(pane.Top),
		})
	}

	return result, nil
}

func assertLivePositionOrder(t *testing.T, model *Model, owner gotmux.Session, want ...gotmux.Pane) int {
	t.Helper()

	index := slices.IndexFunc(model.filtered, func(row session.Session) bool { return row.ID == string(owner.ID()) })
	if index < 0 {
		t.Fatal("source session is absent from the picker")
	}

	names := make([]string, 0, len(want))
	for _, pane := range want {
		names = append(names, string(pane.ID()))
	}

	row := model.filtered[index]
	assertAgentNames(t, row.Agents, names)

	if row.AgentTotal != len(want) || row.AgentActive != len(want) {
		t.Fatalf("agent total/active = %d/%d, want %d/%d", row.AgentTotal, row.AgentActive, len(want), len(want))
	}

	return index
}

func assertPositionSelectionVisible(t *testing.T, model *Model, selected sessionIdentity) {
	t.Helper()

	if model.selectedSessionIdentity() != selected {
		t.Fatal("native pane change moved selection to another agent")
	}

	visible := model.visibleListRows(3)
	if !slices.ContainsFunc(visible, func(row popupListRow) bool { return row.equal(model.selectedListRow()) }) {
		t.Fatal("selected agent scrolled out of view after native pane change")
	}
}
