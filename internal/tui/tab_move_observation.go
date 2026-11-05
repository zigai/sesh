package tui

import (
	"slices"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

const tabMoveRegistryGracePeriod = 30 * time.Second

// A confirmed move can precede aht's next location observation. Only these exact
// agent/pane/process identities may bridge that lag, and only while a fresh tmux
// inventory independently confirms their destination. Normal pruning stays strict.
type agentRelocation struct {
	paneID          string
	panePID         int
	registryID      string
	socket          string
	destinationID   string
	destinationName string
	windowID        string
	expiresAt       time.Time
}

func (m *Model) tabMoveCandidates(subject action.Subject) []session.AgentSession {
	source := subject.Sessions[0]
	for _, owner := range m.sessions {
		if action.LocationOf(owner).Kind == action.LocationLocal && owner.ID == source.ID &&
			sameConfirmedSocket(owner.SemanticIdentity().Socket, source.SemanticIdentity().Socket) {
			return slices.Clone(owner.Agents)
		}
	}

	return slices.Clone(source.Agents)
}

func (m *Model) recordAgentRelocations(
	moved action.MovedWindow,
	candidates []session.AgentSession,
) {
	if !moved.Moved {
		return
	}

	for _, agent := range candidates {
		if agent.PanePID <= 0 || !slices.Contains(moved.PaneIDs, agent.PaneID) {
			continue
		}

		hint := agentRelocation{
			paneID: agent.PaneID, panePID: agent.PanePID, registryID: agent.RegistryID,
			socket: moved.Destination.Socket, destinationID: moved.Destination.ID,
			destinationName: moved.Destination.Name,
			windowID:        moved.Window,
			expiresAt:       time.Now().Add(tabMoveRegistryGracePeriod),
		}
		m.agentRelocations = append(
			slices.DeleteFunc(
				slices.Clone(m.agentRelocations),
				func(previous agentRelocation) bool {
					return time.Now().After(previous.expiresAt) ||
						(previous.paneID == hint.paneID && sameConfirmedSocket(previous.socket, hint.socket))
				},
			),
			hint,
		)
	}
}

func relocateConfirmedAgents(snapshot AgentSnapshot, hints []agentRelocation) AgentSnapshot {
	if !snapshot.PanesObserved || len(hints) == 0 {
		return snapshot
	}

	result := make([]session.AgentSummary, 0, len(snapshot.Summaries))
	for _, original := range snapshot.Summaries {
		for _, agent := range original.Agents {
			owner := original
			if hint, ok := confirmedAgentLocation(original, agent, snapshot.Panes, hints); ok {
				owner.TmuxServerSocket = hint.socket
				owner.TmuxSessionID = hint.destinationID
				owner.TmuxSessionName = hint.destinationName
			}

			index := slices.IndexFunc(result, func(candidate session.AgentSummary) bool {
				return candidate.TmuxSessionID == owner.TmuxSessionID &&
					candidate.TmuxSessionName == owner.TmuxSessionName &&
					sameServerSocket(candidate.TmuxServerSocket, owner.TmuxServerSocket)
			})
			if index < 0 {
				owner.Agents = nil
				result = append(result, owner)
				index = len(result) - 1
			}

			if original.ActivityChangedAt.After(result[index].ActivityChangedAt) {
				result[index].ActivityChangedAt = original.ActivityChangedAt
			}

			result[index].Agents = append(result[index].Agents, agent)
		}
	}

	for index := range result {
		recountAgentSummary(&result[index])
	}

	snapshot.Summaries = result

	return snapshot
}

func confirmedAgentLocation(owner session.AgentSummary, agent session.AgentSession,
	panes []PaneLocation, hints []agentRelocation,
) (agentRelocation, bool) {
	for _, hint := range hints {
		if !hint.matchesAgent(owner, agent) {
			continue
		}

		if slices.ContainsFunc(panes, hint.matchesPane) {
			return hint, true
		}
	}

	return agentRelocation{}, false
}

func (hint agentRelocation) matchesAgent(
	owner session.AgentSummary,
	agent session.AgentSession,
) bool {
	return agent.PaneID == hint.paneID && agent.PanePID == hint.panePID &&
		agent.RegistryID == hint.registryID && sameConfirmedSocket(owner.TmuxServerSocket, hint.socket) &&
		!time.Now().After(hint.expiresAt)
}

func (hint agentRelocation) matchesPane(pane PaneLocation) bool {
	return pane.PaneID == hint.paneID && pane.PanePID == hint.panePID &&
		pane.SessionID == hint.destinationID && pane.SessionName == hint.destinationName &&
		sameConfirmedSocket(pane.ServerSocket, hint.socket) &&
		(hint.windowID == "" || pane.WindowID == hint.windowID)
}

func sameConfirmedSocket(left string, right string) bool {
	return left != "" && right != "" && normalizeServerSocket(left) == normalizeServerSocket(right)
}

func pendingAgentRelocations(snapshot AgentSnapshot, hints []agentRelocation) []agentRelocation {
	return slices.DeleteFunc(slices.Clone(hints), func(hint agentRelocation) bool {
		return time.Now().After(hint.expiresAt) || relocationConverged(snapshot, hint)
	})
}

func relocationConverged(snapshot AgentSnapshot, hint agentRelocation) bool {
	if !snapshot.PanesObserved {
		return false
	}

	for _, owner := range snapshot.Summaries {
		if owner.TmuxSessionID != hint.destinationID ||
			owner.TmuxSessionName != hint.destinationName {
			continue
		}

		for _, agent := range owner.Agents {
			if _, ok := confirmedAgentLocation(
				owner,
				agent,
				snapshot.Panes,
				[]agentRelocation{hint},
			); ok {
				return true
			}
		}
	}

	return false
}
