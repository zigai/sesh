//go:build linux || darwin

package agents

func PruneVanishedPanes(agents []AgentSession, panes []PaneLocation) []AgentSession {
	if len(agents) == 0 {
		return agents
	}

	if len(panes) == 0 {
		return agents
	}

	pruned := make([]AgentSession, 0, len(agents))

	for _, agent := range agents {
		if !agentIsLiveInPane(agent, panes) {
			continue
		}

		pruned = append(pruned, agent)
	}

	return deduplicateAgentsByPane(pruned)
}

func agentIsLiveInPane(agent AgentSession, panes []PaneLocation) bool {
	if agent.PaneID == "" {
		return true
	}

	for _, pane := range FindPanes(panes, agent.PaneID, agent.ServerSocket) {
		if paneMatchesAgentOwner(&agent, pane) {
			return true
		}
	}

	return false
}

func deduplicateAgentsByPane(agents []AgentSession) []AgentSession {
	if len(agents) <= 1 {
		return agents
	}

	seen := make(map[string]int, len(agents))
	result := make([]AgentSession, 0, len(agents))

	for _, agent := range agents {
		if agent.PaneID == "" {
			result = append(result, agent)

			continue
		}

		index, exists := seen[agent.PaneID]
		if exists {
			if agent.ActivityChanged.After(result[index].ActivityChanged) {
				result[index] = agent
			}

			continue
		}

		seen[agent.PaneID] = len(result)
		result = append(result, agent)
	}

	return result
}
