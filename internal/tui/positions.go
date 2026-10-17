package tui

import (
	"strconv"
	"time"

	"github.com/zigai/sesh/internal/session"
)

type PaneLocation struct {
	ServerSocket string
	SessionID    string
	SessionName  string
	WindowID     string
	WindowIndex  string
	PaneID       string
	PaneIndex    string
	PanePID      int
	Command      string
	PaneLeft     string
	PaneTop      string
}

func retainAgentPositions(agents []session.AgentSession, previous []session.AgentSession) {
	byPane := make(map[string]session.AgentSession, len(previous))
	for _, agent := range previous {
		if agent.PositionKnown && agent.PaneID != "" {
			byPane[agent.PaneID] = agent
		}
	}

	for index := range agents {
		agent := &agents[index]
		if agent.PositionKnown {
			continue
		}

		position, ok := byPane[agent.PaneID]
		if !ok || position.PanePID != agent.PanePID || position.RegistryID != agent.RegistryID {
			continue
		}

		agent.PositionKnown = true
		agent.WindowIndex = position.WindowIndex
		agent.PaneLeft = position.PaneLeft
		agent.PaneTop = position.PaneTop
	}
}

func applyPanePosition(summary *session.AgentSummary, agent *session.AgentSession, panes []PaneLocation) bool {
	agent.PositionKnown = false

	for _, pane := range panes {
		if !paneMatchesAgentOwner(summary, agent, pane) {
			continue
		}

		window, windowErr := strconv.Atoi(pane.WindowIndex)
		left, leftErr := strconv.Atoi(pane.PaneLeft)

		top, topErr := strconv.Atoi(pane.PaneTop)
		if windowErr == nil && leftErr == nil && topErr == nil && window >= 0 && left >= 0 && top >= 0 {
			agent.PositionKnown = true
			agent.WindowIndex = window
			agent.PaneLeft = left
			agent.PaneTop = top
		}

		return true
	}

	return false
}

func paneMatchesAgentOwner(summary *session.AgentSummary, agent *session.AgentSession, pane PaneLocation) bool {
	if pane.PaneID != agent.PaneID || !sameServerSocket(pane.ServerSocket, summary.TmuxServerSocket) {
		return false
	}

	if summary.TmuxSessionID != "" && pane.SessionID != summary.TmuxSessionID {
		return false
	}

	if summary.TmuxSessionName != "" && pane.SessionName != summary.TmuxSessionName {
		return false
	}

	return agent.PanePID <= 0 || pane.PanePID <= 0 || agent.PanePID == pane.PanePID
}

func pruneAgentsWithVanishedPanes(summaries []session.AgentSummary, panes []PaneLocation) []session.AgentSummary {
	result := make([]session.AgentSummary, 0, len(summaries))

	for _, summary := range summaries {
		kept := make([]session.AgentSession, 0, len(summary.Agents))

		for _, agent := range summary.Agents {
			if agent.PaneID == "" || !applyPanePosition(&summary, &agent, panes) {
				continue
			}

			kept = append(kept, agent)
		}

		summary.Agents = kept
		if len(kept) == 0 {
			continue
		}

		recountAgentSummary(&summary)
		result = append(result, summary)
	}

	return result
}

func recountAgentSummary(summary *session.AgentSummary) {
	summary.Total = len(summary.Agents)
	summary.Active = 0
	summary.Waiting = 0
	summary.Failed = 0
	summary.Interrupted = 0
	summary.Unknown = 0
	summary.IdleSince = time.Time{}
	summary.WaitingSince = time.Time{}

	for _, agent := range summary.Agents {
		switch agent.Status {
		case session.AgentStatusRunning:
			summary.Active++
		case session.AgentStatusWaiting:
			summary.Waiting++
			summary.WaitingSince = earlierAgentStatus(summary.WaitingSince, agent.StatusSince)
		case session.AgentStatusIdle:
			summary.IdleSince = earlierAgentStatus(summary.IdleSince, agent.StatusSince)
		case session.AgentStatusFailed:
			summary.Failed++
		case session.AgentStatusInterrupted:
			summary.Interrupted++
			summary.IdleSince = earlierAgentStatus(summary.IdleSince, agent.StatusSince)
		case session.AgentStatusUnknown, session.AgentStatusSleeping, session.AgentStatusMerged:
			summary.Unknown++
		default:
			summary.Unknown++
		}
	}
}

func earlierAgentStatus(current, next time.Time) time.Time {
	if current.IsZero() || (!next.IsZero() && next.Before(current)) {
		return next
	}

	return current
}
