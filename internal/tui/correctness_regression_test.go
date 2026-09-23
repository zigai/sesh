package tui

import (
	"testing"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentSummaryIndexPreservesReusedTmuxIDCompositeIdentity(t *testing.T) {
	t.Parallel()

	summaries := []session.AgentSummary{
		{TmuxSessionID: "$4", TmuxSessionName: "kwinl", Total: 1, Active: 1},
		{TmuxSessionID: "$4", TmuxSessionName: "lab", Total: 1, Active: 0},
	}
	byID, byName := indexAgentSummaries(summaries)
	row := session.Session{ID: "$4", Name: "kwinl"}
	applyAgentSummaryToSession(&row, byID, byName)

	if row.AgentTotal != 1 || row.AgentActive != 1 {
		t.Fatalf("exact ($4,kwinl) summary was dropped: active/total = %d/%d", row.AgentActive, row.AgentTotal)
	}
}

func TestAgentSummaryIndexUsesTmuxServerIdentity(t *testing.T) {
	t.Parallel()

	summaries := []session.AgentSummary{
		{
			TmuxServerSocket: "/tmp/server-a/tmux-1000/default",
			TmuxSessionID:    "$1",
			TmuxSessionName:  testSupportSessionDevName,
			Total:            1,
			Active:           1,
		},
		{
			TmuxServerSocket: "/tmp/server-b/tmux-1000/default",
			TmuxSessionID:    "$1",
			TmuxSessionName:  testSupportSessionDevName,
			Total:            2,
			Waiting:          2,
		},
	}
	byID, byName := indexAgentSummaries(summaries)
	row := session.Session{
		ID:               "$1",
		Name:             testSupportSessionDevName,
		TmuxServerSocket: "/tmp/server-a/tmux-1000/default",
	}
	applyAgentSummaryToSession(&row, byID, byName)

	if row.AgentTotal != 1 || row.AgentActive != 1 || row.AgentWaiting != 0 {
		t.Fatalf("server-a summary = active/waiting/total %d/%d/%d, want 1/0/1",
			row.AgentActive,
			row.AgentWaiting,
			row.AgentTotal,
		)
	}
}
