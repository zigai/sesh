package tui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/zigai/sesh/internal/session"
)

const (
	agentSortPosition  agentSort = "position"
	agentSortName      agentSort = "name"
	agentSortAttention agentSort = "attention"
)

const (
	rankWaiting = 0
	rankFailed  = 1
	rankIdle    = 2
	rankRunning = 3
	rankUnknown = 4
)

type agentSort string

func parseAgentSort(value string) agentSort {
	switch agentSort(strings.ToLower(strings.TrimSpace(value))) {
	case agentSortName:
		return agentSortName
	case agentSortAttention:
		return agentSortAttention
	case agentSortPosition:
		return agentSortPosition
	default:
		return agentSortPosition
	}
}

func (order agentSort) label() string {
	switch order {
	case agentSortName:
		return "name"
	case agentSortAttention:
		return "attention"
	case agentSortPosition:
		return "position"
	default:
		return "position"
	}
}

func sortAgentSessions(agents []session.AgentSession, order agentSort) {
	slices.SortStableFunc(agents, func(left session.AgentSession, right session.AgentSession) int {
		if order == agentSortName {
			return compareAgentSessions(left, right)
		}

		if order == agentSortAttention {
			if priority := cmp.Compare(agentAttentionRank(left.Status), agentAttentionRank(right.Status)); priority != 0 {
				return priority
			}

			if agentAttentionRank(left.Status) <= rankIdle {
				if age := compareAgentStatusSince(left, right); age != 0 {
					return age
				}
			}
		}

		if position := compareAgentPositions(left, right); position != 0 {
			return position
		}

		return compareAgentSessions(left, right)
	})
}

func compareAgentPositions(left session.AgentSession, right session.AgentSession) int {
	if left.PositionKnown != right.PositionKnown {
		if left.PositionKnown {
			return -1
		}

		return 1
	}

	if !left.PositionKnown {
		return 0
	}

	for _, coordinates := range [][2]int{
		{left.WindowIndex, right.WindowIndex},
		{left.PaneLeft, right.PaneLeft},
		{left.PaneTop, right.PaneTop},
	} {
		if position := cmp.Compare(coordinates[0], coordinates[1]); position != 0 {
			return position
		}
	}

	return 0
}

func agentAttentionRank(status session.AgentStatus) int {
	switch status.DisplayStatus() {
	case session.AgentStatusWaiting:
		return rankWaiting
	case session.AgentStatusFailed:
		return rankFailed
	case session.AgentStatusIdle, session.AgentStatusInterrupted:
		return rankIdle
	case session.AgentStatusRunning:
		return rankRunning
	case session.AgentStatusUnknown:
		return rankUnknown
	default:
		return rankUnknown
	}
}

func compareAgentStatusSince(left session.AgentSession, right session.AgentSession) int {
	if left.StatusSince.IsZero() != right.StatusSince.IsZero() {
		if left.StatusSince.IsZero() {
			return 1
		}

		return -1
	}

	return left.StatusSince.Compare(right.StatusSince)
}

func (m *Model) setAgentSort(order agentSort) {
	selected := m.selectedSessionIdentity()
	m.listViewportPolicy = listViewportFollowSelection
	m.agentSort = order
	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}
