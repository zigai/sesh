package tui

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentPruningRejectsMissingAndConflictingPaneOwners(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		panes []PaneLocation
		want  int
	}{
		{name: "empty observation", panes: nil, want: 0},
		{name: "missing pane", panes: []PaneLocation{{PaneID: "%2"}}, want: 0},
		{name: "different server", panes: []PaneLocation{{ServerSocket: "/other", SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 100}}, want: 0},
		{name: "different session", panes: []PaneLocation{{ServerSocket: "/test", SessionID: "$2", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 100}}, want: 0},
		{name: "different name", panes: []PaneLocation{{ServerSocket: "/test", SessionID: "$1", SessionName: "other", PaneID: "%1", PanePID: 100}}, want: 0},
		{name: "replaced process", panes: []PaneLocation{{ServerSocket: "/test", SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 101}}, want: 0},
		{name: "matching owner", panes: []PaneLocation{{ServerSocket: "/test", SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 100}}, want: 1},
		{name: "unknown process", panes: []PaneLocation{{ServerSocket: "/test", SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1"}}, want: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			summaries := []session.AgentSummary{{
				TmuxServerSocket: "/test", TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Total: 1,
				Agents: []session.AgentSession{{RegistryID: "live", PaneID: "%1", PanePID: 100}},
			}}

			got := pruneAgentsWithVanishedPanes(summaries, testCase.panes)
			if len(got) != testCase.want {
				t.Fatalf("retained summaries = %#v, want %d", got, testCase.want)
			}

			if summaries[0].Agents[0].PositionKnown || len(summaries[0].Agents) != 1 {
				t.Fatal("pruning mutated the source summary")
			}
		})
	}
}

func TestAgentPruningRecountsSurvivingStatusesAndClocks(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	statuses := []session.AgentStatus{
		session.AgentStatusRunning, session.AgentStatusWaiting, session.AgentStatusIdle,
		session.AgentStatusFailed, session.AgentStatusInterrupted, session.AgentStatusUnknown,
	}
	summary := session.AgentSummary{
		TmuxSessionID: "$1", Total: 99, Active: 99, Waiting: 99, Failed: 99, Interrupted: 99,
		Unknown: 99, IdleSince: now.Add(-time.Hour), WaitingSince: now.Add(-time.Hour),
	}

	panes := make([]PaneLocation, 0, len(statuses))
	for index, status := range statuses {
		paneID := string(rune('a' + index))
		summary.Agents = append(summary.Agents, session.AgentSession{PaneID: paneID, Status: status, StatusSince: now})
		panes = append(panes, PaneLocation{SessionID: "$1", PaneID: paneID, WindowIndex: "2", PaneLeft: "4", PaneTop: "6"})
	}

	summary.Agents = append(summary.Agents, session.AgentSession{PaneID: "%dead", Status: session.AgentStatusWaiting, StatusSince: now.Add(-time.Hour)})

	got := pruneAgentsWithVanishedPanes([]session.AgentSummary{summary}, panes)
	if len(got) != 1 {
		t.Fatalf("retained summaries = %#v, want one", got)
	}

	kept := got[0]

	counts := [6]int{kept.Total, kept.Active, kept.Waiting, kept.Failed, kept.Interrupted, kept.Unknown}
	if counts != [6]int{6, 1, 1, 1, 1, 1} {
		t.Fatalf("surviving counts = %#v, want six agents, one of each status", kept)
	}

	if !kept.IdleSince.Equal(now) || !kept.WaitingSince.Equal(now) {
		t.Fatalf("surviving clocks = %v/%v, want %v", kept.IdleSince, kept.WaitingSince, now)
	}

	for _, agent := range kept.Agents {
		if !agent.PositionKnown || agent.WindowIndex != 2 || agent.PaneLeft != 4 || agent.PaneTop != 6 {
			t.Fatalf("surviving geometry = %#v, want 2/4/6", agent)
		}
	}
}
