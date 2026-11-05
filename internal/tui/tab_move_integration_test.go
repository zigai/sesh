//go:build (linux || darwin) && integration

package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
)

func TestTabMoveMenuThroughDispatcherRefreshesLiveAgentOwnership(t *testing.T) {
	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			server := tmuxtest.NewServer(t)
			source := newPositionSession(t, server, "source")
			destination := newPositionSession(t, server, "destination")
			first := positionSessionPane(t, source)

			second, err := first.Split(
				t.Context(),
				gotmux.SplitOptions{Direction: gotmux.DirectionHorizontal},
			)
			if err != nil {
				t.Fatal(err)
			}

			firstInfo, err := first.Info(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			secondInfo, err := second.Info(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			registry := []session.AgentSession{
				{
					PaneID: string(first.ID()), RegistryID: "agent-first", PanePID: firstInfo.PID,
					Harness: "pi", HarnessSession: "first", SessionID: "conversation-first",
					Status: session.AgentStatusRunning, StatusSince: time.Unix(1, 0),
				},
				{
					PaneID: string(
						second.ID(),
					),
					RegistryID:     "agent-second",
					PanePID:        secondInfo.PID,
					Harness:        "pi",
					HarnessSession: "second",
					SessionID:      "conversation-second",
					Status:         session.AgentStatusRunning,
					StatusSince:    time.Unix(1, 0),
				},
			}
			model := newTestModelWithDeps(Dependencies{
				LoadCatalog: func(ctx context.Context) (session.CatalogResult, error) {
					return session.ListCatalog(ctx, session.CatalogOptions{Server: server})
				},
				LoadAgentSummaries: func(ctx context.Context) ([]session.AgentSummary, error) {
					// Simulate aht location lag: pane observations are fresh, but the
					// registry still reports the original owner after the move.
					summaries := movedLiveAgentSummaries(t, ctx, server, registry)
					for i := range summaries {
						summaries[i].TmuxSessionID = string(source.ID())
						summaries[i].TmuxSessionName = "source"
					}

					return summaries, nil
				},
				TmuxPaneLocations: func(ctx context.Context) ([]PaneLocation, error) {
					return observePositionPanes(ctx, server)
				},
			})
			t.Cleanup(model.Close)
			model.mode = modePopup
			model.displayMode = mode
			model.width, model.height = 120, 30
			model.config.Inbox.Collapsed = nil
			metadata := inbox.State{
				Version: inbox.Version,
				Unseen:  []inbox.Unseen{{Key: "untouched", At: 123}},
			}
			model.inbox = metadata
			model.deps.Inbox = &memoryInbox{state: metadata}

			historyDir := filepath.Join(t.TempDir(), "history")
			if err := os.Mkdir(historyDir, 0o700); err != nil {
				t.Fatal(err)
			}

			local := app.NewLocal(app.LocalOptions{
				Config: model.config, Server: server,
				HistoryPath: filepath.Join(historyDir, "history.json"), LeaseRoot: t.TempDir(),
			})

			dependenciesForTest(model).RunAction = local.Dispatch
			if err := model.loadSessions(); err != nil {
				t.Fatal(err)
			}

			selectLiveAgentPane(t, model, string(first.ID()))
			beginTestTabMove(t, model)
			selectTabMoveDestination(t, model, string(destination.ID()))

			trace := pressTestKey(t, model, keyCode(tea.KeyEnter))
			if trace.quit || trace.entered != nil || len(trace.results) != 1 ||
				trace.results[0].err != nil {
				t.Fatalf("move trace = %+v", trace)
			}

			moved, ok := trace.results[0].output.(action.MovedWindow)
			if !ok || !moved.Moved || len(moved.PaneIDs) != 2 {
				t.Fatalf("move result = %+v", moved)
			}

			if model.tabMove != nil || model.movedAgentSelection != nil ||
				model.selectedSessionIdentity().paneID != string(first.ID()) {
				t.Fatalf("agent selection after refresh = %+v", model.selectedSessionIdentity())
			}

			if !reflect.DeepEqual(model.inbox, metadata) {
				t.Fatal("moving changed Inbox metadata")
			}

			for _, pane := range []gotmux.Pane{first, second} {
				info, err := pane.Info(t.Context())
				if err != nil {
					t.Fatal(err)
				}

				owner, present := info.SessionID.Get()
				if !present || owner != destination.ID() {
					t.Fatalf("pane owner = %s, want %s", owner, destination.ID())
				}

				old := firstInfo
				if pane.ID() == second.ID() {
					old = secondInfo
				}

				if info.PID != old.PID {
					t.Fatal("moving restarted a pane process")
				}
			}

			for _, row := range model.sessions {
				if row.ID == string(source.ID()) {
					t.Fatal("last-window source remained in refreshed catalog")
				}

				if row.ID == string(destination.ID()) && len(row.Agents) != 2 {
					t.Fatal("destination did not receive both coding agents")
				}
			}
		})
	}
}

func selectLiveAgentPane(t *testing.T, model *Model, paneID string) {
	t.Helper()

	for si, row := range model.filtered {
		for ai, agent := range row.Agents {
			if agent.PaneID == paneID {
				model.selectListRow(popupListRow{sessionIndex: si, agentIndex: ai})
				return
			}
		}
	}

	t.Fatalf("agent pane %s missing from view", paneID)
}

func movedLiveAgentSummaries(t *testing.T, ctx context.Context, server *gotmux.Server,
	registry []session.AgentSession,
) []session.AgentSummary {
	t.Helper()

	panes, err := observePositionPanes(ctx, server)
	if err != nil {
		t.Fatal(err)
	}

	var summaries []session.AgentSummary

	for _, agent := range registry {
		index := slices.IndexFunc(
			panes,
			func(pane PaneLocation) bool { return pane.PaneID == agent.PaneID },
		)
		if index < 0 {
			continue
		}

		pane := panes[index]

		owner := slices.IndexFunc(
			summaries,
			func(summary session.AgentSummary) bool { return summary.TmuxSessionID == pane.SessionID },
		)
		if owner < 0 {
			summaries = append(summaries, session.AgentSummary{
				TmuxSessionID:   pane.SessionID,
				TmuxSessionName: pane.SessionName, TmuxServerSocket: pane.ServerSocket,
			})
			owner = len(summaries) - 1
		}

		summaries[owner].Agents = append(summaries[owner].Agents, agent)
		recountAgentSummary(&summaries[owner])
	}

	return summaries
}
