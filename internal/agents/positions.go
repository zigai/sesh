//go:build linux || darwin

package agents

import "strconv"

func AttachPanePositions(agents []AgentSession, panes []PaneLocation) []AgentSession {
	result := make([]AgentSession, len(agents))

	copy(result, agents)

	for index := range result {
		applyPanePosition(&result[index], panes)
	}

	return result
}

func applyPanePosition(agent *AgentSession, panes []PaneLocation) {
	pane, ok := FindPane(panes, agent.PaneID, agent.ServerSocket)
	if !ok {
		return
	}

	if !paneMatchesAgentOwner(agent, pane) {
		return
	}

	if pane.WindowIndex == "" {
		return
	}

	window, err := strconv.Atoi(pane.WindowIndex)
	if err != nil || window < 0 {
		return
	}

	agent.WindowIndex = strconv.Itoa(window)
	agent.PaneIndex = pane.PaneIndex
}

func paneMatchesAgentOwner(agent *AgentSession, pane PaneLocation) bool {
	if pane.PaneID != agent.PaneID || !SameServerSocket(pane.ServerSocket, agent.ServerSocket) {
		return false
	}

	if agent.SessionID != "" && pane.SessionID != "" && pane.SessionID != agent.SessionID {
		return false
	}

	if agent.SessionName != "" && pane.SessionName != "" && pane.SessionName != agent.SessionName {
		return false
	}

	return agent.PanePID <= 0 || pane.PanePID <= 0 || agent.PanePID == pane.PanePID
}
