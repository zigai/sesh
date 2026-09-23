package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

type agentPositionTickMsg struct{}

type agentPositionsMsg struct {
	panes []PaneLocation
	ready bool
}

func (m *Model) agentPositionTickCommand() tea.Cmd {
	if m.mode != modePopup || m.deps.Panes == nil || !agentSummariesEnabled(m.config, m.deps) {
		return nil
	}

	return tea.Tick(time.Second, func(time.Time) tea.Msg { return agentPositionTickMsg{} })
}

func (m *Model) loadAgentPositionsCommand() tea.Cmd {
	if m.agentSort == agentSortName || m.displayMode != displayModeAgents || m.loadingSessions || m.deps.Panes == nil {
		return m.agentPositionTickCommand()
	}

	ctx := m.operationContext()
	panes := m.deps.Panes

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		locations, err := panes.TmuxPaneLocations(ctx)

		return agentPositionsMsg{panes: locations, ready: err == nil}
	}
}

func (m *Model) applyAgentPositions(msg agentPositionsMsg) {
	if !msg.ready {
		return
	}

	selected := m.selectedSessionIdentity()
	for index := range m.sessions {
		row := &m.sessions[index]
		if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge || len(row.Agents) == 0 {
			continue
		}

		summary := session.AgentSummary{
			TmuxServerSocket:  row.TmuxServerSocket,
			TmuxSessionID:     row.ID,
			TmuxSessionName:   row.Name,
			Total:             0,
			Active:            0,
			Waiting:           0,
			Failed:            0,
			Interrupted:       0,
			Unknown:           0,
			IdleSince:         time.Time{},
			WaitingSince:      time.Time{},
			ActivityChangedAt: time.Time{},
			Agents:            row.Agents,
		}

		for index := range summary.Agents {
			applyPanePosition(&summary, &summary.Agents[index], msg.panes)
		}

		row.Agents = summary.Agents
	}

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}
