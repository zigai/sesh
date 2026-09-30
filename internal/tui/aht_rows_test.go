package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentRowsRenderCurrentReasonsAndSuppressStaleReasons(t *testing.T) {
	at := time.Unix(500, 0)

	for _, width := range []int{58, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			model := newTestModel()
			t.Cleanup(model.Close)
			model.displayMode = displayModeAgents
			model.config.Display.AgentsAgentFormat = "{tree}{status_text} {agent_waiting_reason} {agent_failure_reason}{stale} {agent_session}"

			formats, err := compileSessionRowFormats(model.config.Display, model.config.Settings)
			if err != nil {
				t.Fatal(err)
			}

			model.sessionFormats = formats
			model.filtered = []session.Session{{Name: "project", AgentTotal: 4, Agents: []session.AgentSession{
				{PaneID: "%1", HarnessSession: "approval", Status: session.AgentStatusWaiting, WaitingReason: "permission", Detail: aht.StateDetail{Value: aht.ActivityDetailPermission, Quality: aht.DetailQualityCurrent, Authority: "hook", ObservedAt: at}},
				{PaneID: "%2", HarnessSession: "limit", Status: session.AgentStatusFailed, FailureReason: "usage_limit", Detail: aht.StateDetail{Value: aht.ActivityDetailUsageLimit, Quality: aht.DetailQualityCurrent, Authority: "screen", ObservedAt: at}},
				{PaneID: "%3", HarnessSession: "old", Status: session.AgentStatusWaiting, WaitingReason: "question", Detail: aht.StateDetail{Value: aht.ActivityDetailQuestion, Quality: aht.DetailQualityStale, Authority: "hook", ObservedAt: at}},
				{PaneID: "%4", HarnessSession: "stopped", Status: session.AgentStatusInterrupted},
			}}}

			output := stripANSI(model.renderList(width, 5))
			requireTextContainsAll(t, output, "WAITING", "permission", "ERROR", "usage_limit", "~", "IDLE")

			if strings.Contains(output, "question") || strings.Contains(output, "PAUSED") {
				t.Fatalf("render exposed stale detail or a fifth state: %q", output)
			}

			for line := range strings.SplitSeq(output, "\n") {
				if lipgloss.Width(line) != width {
					t.Fatalf("render width = %d, want %d: %q", lipgloss.Width(line), width, line)
				}
			}
		})
	}
}

func TestInterruptedAgentUsesIdleSortingSearchAndCounts(t *testing.T) {
	at := time.Unix(500, 0)
	agents := []session.AgentSession{
		{HarnessSession: "new interrupted", Status: session.AgentStatusInterrupted, StatusSince: at},
		{HarnessSession: "old idle", Status: session.AgentStatusIdle, StatusSince: at.Add(-time.Minute)},
	}
	sortAgentSessions(agents, agentSortAttention)

	if agents[0].HarnessSession != "old idle" || !agentStatusMatches("idle", session.AgentStatusInterrupted) {
		t.Fatalf("interrupted agent differs from idle: %+v", agents)
	}

	summary := session.AgentSummary{Agents: agents}
	recountAgentSummary(&summary)

	if summary.Interrupted != 1 || !summary.IdleSince.Equal(at.Add(-time.Minute)) {
		t.Fatalf("idle summary = %+v", summary)
	}

	if count := agentIdleCount(session.Session{AgentTotal: 2, AgentInterrupted: 1}); count != 2 {
		t.Fatalf("display idle count = %d, want 2", count)
	}
}
