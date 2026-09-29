//go:build linux || darwin

package agents

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
