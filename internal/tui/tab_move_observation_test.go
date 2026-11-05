package tui

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestConfirmedTabMoveBridgesRegistryLocationLagOnlyWithFreshMatchingProcess(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*AgentSnapshot)
		wantMoved bool
	}{
		{name: "confirmed destination", wantMoved: true},
		{name: "no pane inventory", change: func(s *AgentSnapshot) { s.PanesObserved = false }},
		{name: "missing pane", change: func(s *AgentSnapshot) { s.Panes = nil }},
		{name: "replaced process", change: func(s *AgentSnapshot) { s.Panes[0].PanePID++ }},
		{name: "unknown process", change: func(s *AgentSnapshot) { s.Panes[0].PanePID = 0 }},
		{name: "different server", change: func(s *AgentSnapshot) { s.Panes[0].ServerSocket = "/other/socket" }},
		{name: "different destination", change: func(s *AgentSnapshot) { s.Panes[0].SessionID = "$3" }},
		{name: "different agent", change: func(s *AgentSnapshot) {
			s.Summaries[0].Agents[0].RegistryID = "replacement"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := AgentSnapshot{
				Summaries: []session.AgentSummary{
					{
						TmuxServerSocket: "/test/socket",
						TmuxSessionID:    "$1",
						TmuxSessionName:  "source",
						Agents: []session.AgentSession{
							{
								PaneID:     "%1",
								PanePID:    100,
								RegistryID: "agent",
								Status:     session.AgentStatusRunning,
							},
						},
					},
				},
				Panes: []PaneLocation{
					{
						ServerSocket: "/test/socket",
						SessionID:    "$2",
						SessionName:  "destination",
						PaneID:       "%1",
						PanePID:      100,
					},
				},
				PanesObserved: true,
			}
			if test.change != nil {
				test.change(&snapshot)
			}

			before := cloneAgentSummaries(snapshot.Summaries)
			hints := []agentRelocation{
				{
					paneID:          "%1",
					panePID:         100,
					registryID:      "agent",
					socket:          "/test/socket",
					destinationID:   "$2",
					destinationName: "destination",
					expiresAt:       time.Now().Add(time.Minute),
				},
			}
			reconciled := relocateConfirmedAgents(snapshot, hints)

			moved := reconciled.Summaries[0].TmuxSessionID == "$2"
			if moved != test.wantMoved {
				t.Fatalf("relocated = %t, want %t", moved, test.wantMoved)
			}

			if !reflect.DeepEqual(before, snapshot.Summaries) {
				t.Fatal("relocation mutated shared registry snapshot")
			}

			if test.wantMoved {
				live := liveAgentSummaries(reconciled)
				if len(live) != 1 || live[0].Total != 1 || live[0].Active != 1 ||
					live[0].Agents[0].RegistryID != "agent" {
					t.Fatalf("confirmed moved agent was dropped or changed: %+v", live)
				}
			}
		})
	}
}

func TestConfirmedTabMoveReconcilesWatchSnapshotAndCannotOverrideAnotherMove(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.sessions[0].Agents[0].PanePID = 100
	moved := action.MovedWindow{
		Source: action.RefOf(model.sessions[0]), Destination: action.RefOf(model.sessions[1]),
		PaneIDs: []string{"%1"}, Moved: true,
	}
	model.recordAgentRelocations(moved, model.sessions[0].Agents)
	snapshot := AgentSnapshot{
		Summaries: []session.AgentSummary{{
			TmuxServerSocket: model.sessions[0].TmuxServerSocket,
			TmuxSessionID:    "$1", TmuxSessionName: "source", Agents: model.sessions[0].Agents,
		}},
		Panes: []PaneLocation{
			{
				ServerSocket: model.sessions[0].TmuxServerSocket,
				SessionID:    "$2",
				SessionName:  "destination",
				PaneID:       "%1",
				PanePID:      100,
			},
		}, PanesObserved: true,
	}
	model.handleAgentSummaries(agentSummariesMsg{snapshot: &snapshot, revision: 1})

	for _, row := range model.sessions {
		if row.ID == "$1" && len(row.Agents) != 0 {
			t.Fatal("stale watcher put moved agent back in source")
		}

		if row.ID == "$2" && (len(row.Agents) != 1 || row.Agents[0].PaneID != "%1") {
			t.Fatal("watcher dropped confirmed moved agent")
		}
	}
	// An unrelated later tmux move is not authorized by the old confirmation.
	snapshot.Panes[0].SessionID = "$3"
	snapshot.Panes[0].SessionName = "empty"
	model.handleAgentSummaries(agentSummariesMsg{snapshot: &snapshot, revision: 2})

	for _, row := range model.sessions {
		if len(row.Agents) != 0 {
			t.Fatal("old confirmation overrode a subsequent unconfirmed pane move")
		}
	}
}

func TestConfirmedTabMoveExceptionExpiresAndSettlesOnRegistryConvergence(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.sessions[0].Agents[0].PanePID = 100
	model.recordAgentRelocations(action.MovedWindow{
		Source:      action.RefOf(model.sessions[0]),
		Destination: action.RefOf(model.sessions[1]), PaneIDs: []string{"%1"}, Moved: true,
	}, model.sessions[0].Agents)

	if len(model.agentRelocations) != 1 {
		t.Fatal("missing confirmed relocation")
	}

	hint := model.agentRelocations[0]

	snapshot := AgentSnapshot{
		Summaries: []session.AgentSummary{
			{
				TmuxServerSocket: hint.socket,
				TmuxSessionID:    hint.destinationID,
				TmuxSessionName:  hint.destinationName,
				Agents: []session.AgentSession{
					{PaneID: hint.paneID, PanePID: hint.panePID, RegistryID: hint.registryID},
				},
			},
		},
		Panes: []PaneLocation{
			{
				ServerSocket: hint.socket,
				SessionID:    hint.destinationID,
				SessionName:  hint.destinationName,
				PaneID:       hint.paneID,
				PanePID:      hint.panePID,
			},
		}, PanesObserved: true,
	}
	if pending := pendingAgentRelocations(snapshot, model.agentRelocations); len(pending) != 0 {
		t.Fatal("converged registry retained a move exception")
	}

	model.agentRelocations[0].expiresAt = time.Now().Add(-time.Second)
	if pending := pendingAgentRelocations(
		AgentSnapshot{},
		model.agentRelocations,
	); len(
		pending,
	) != 0 {
		t.Fatal("expired exception retained")
	}
}

func TestTabMoveCapturesAllSourceAgentsBeforeWatcherCanPruneThem(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.sessions[0].Agents[0].PanePID = 100
	model.sessions[0].Agents = append(model.sessions[0].Agents,
		session.AgentSession{PaneID: "%9", PanePID: 900, RegistryID: "sibling"},
		session.AgentSession{PaneID: "%8", PanePID: 800, RegistryID: "other-window"})
	model.refreshDisplaySessions()
	model.filterSessions()
	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})
	source := action.RefOf(model.sessions[0])
	destination := action.RefOf(model.sessions[1])
	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, destination.ID)
	model.confirmTabMove()
	// A watcher runs after tmux has moved the window, before its action result
	// reaches the update loop. With no confirmed hint yet, normal pruning wins.
	agents := slices.Clone(model.sessions[0].Agents)
	snapshot := AgentSnapshot{
		Summaries: []session.AgentSummary{{
			TmuxServerSocket: source.Socket,
			TmuxSessionID:    source.ID, TmuxSessionName: source.Name, Agents: agents,
		}},
		Panes: []PaneLocation{
			{
				ServerSocket: destination.Socket,
				SessionID:    destination.ID,
				SessionName:  destination.Name,
				PaneID:       "%1",
				PanePID:      100,
			},
			{
				ServerSocket: destination.Socket, SessionID: destination.ID,
				SessionName: destination.Name, PaneID: "%9", PanePID: 900,
			},
		}, PanesObserved: true,
	}
	model.handleAgentSummaries(agentSummariesMsg{snapshot: &snapshot, revision: 1})

	if len(model.sessions[0].Agents) != 0 {
		t.Fatal("test did not simulate pruning before completion")
	}

	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output: action.MovedWindow{
			Source:      source,
			Destination: destination,
			PaneIDs:     []string{"%1", "%9"},
			Moved:       true,
		},
	})

	if len(model.agentRelocations) != 2 {
		t.Fatalf(
			"captured relocation identities = %d, want both moved agents",
			len(model.agentRelocations),
		)
	}

	model.handleAgentSummaries(agentSummariesMsg{snapshot: &snapshot, revision: 2})

	for _, row := range model.sessions {
		if row.ID == destination.ID && len(row.Agents) != 2 {
			t.Fatal("watcher-before-result ordering lost moved agents")
		}
	}
}

func TestTabMoveRejectsPrecompletionPollAndCatalogResults(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	oldRows := slices.Clone(model.sessions)
	oldObservation := model.agentSummaryObservation
	oldMoveObservation := model.tabMoveObservation
	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, "$2")
	model.confirmTabMove()
	model.handleActionResult(actionResultMsg{
		request: pendingAction{entry: action.SessionMoveWindow},
		output:  action.MovedWindow{Moved: true},
	})

	if model.agentSummaryObservation == oldObservation ||
		model.tabMoveObservation == oldMoveObservation {
		t.Fatal("move did not invalidate precompletion observations")
	}

	rows := slices.Clone(oldRows)
	rows[1].Agents = append(slices.Clone(rows[1].Agents), rows[0].Agents...)
	rows[0].Agents = nil
	model.applySessionRows(sessionLoadResultMsg{
		sessions: rows, moveObservation: model.tabMoveObservation,
		agentObservation: model.agentSummaryObservation,
	})
	before := slices.Clone(model.sessions)
	selected := model.selectedSessionIdentity()
	model.handleAgentSummaries(agentSummariesMsg{polled: true, observation: oldObservation})
	model.handleSessionLoadResult(sessionLoadResultMsg{
		sessions: oldRows, moveObservation: oldMoveObservation,
		catalogErr: errTmuxUnavailable,
	})

	if !reflect.DeepEqual(before, model.sessions) || model.selectedSessionIdentity() != selected ||
		model.statusLevel == statusError {
		t.Fatal("stale poll or catalog result overwrote the completed move")
	}
}
