package tui

import (
	"context"
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/display"

	"github.com/zigai/sesh/internal/session"
)

type agentGitTickMsg struct{}

type agentGitResultMsg struct {
	request uint64
	entries map[string]agentGitCacheEntry
}

func (m *Model) agentGitEnabled() bool {
	return m.mode == modePopup && agentSummariesEnabled(m.config, m.deps) &&
		m.sessionFormats.agentsAgent.UsesAny(
			display.FieldAgentGitBranch, display.FieldAgentGitGlyph, display.FieldAgentGitChanges,
			display.FieldAgentGitInfo, display.FieldAgentGitLabel,
			display.FieldAgentGitAdded, display.FieldAgentGitDeleted,
			display.FieldAgentGitAhead, display.FieldAgentGitBehind,
			display.FieldAgentWorktree, display.FieldAgentWorktreeName,
		)
}

func (m *Model) agentGitTickCommand() tea.Cmd {
	if !m.agentGitEnabled() {
		return nil
	}

	return tea.Tick(agentGitTickInterval, func(time.Time) tea.Msg { return agentGitTickMsg{} })
}

func (m *Model) loadAgentGitCommand() tea.Cmd {
	if !m.agentGitEnabled() || m.gitInFlight {
		return m.agentGitTickCommand()
	}

	directories := m.agentGitDirectories()
	if len(directories) == 0 {
		return m.agentGitTickCommand()
	}

	m.gitInFlight = true
	m.agentGitRequest++
	request := m.agentGitRequest

	ctx, cancel := context.WithCancel(m.operationContext())
	m.agentGitCancel = cancel
	scan := m.gitScan
	previous := m.agentGit
	cachePath := agentGitCachePath()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		entries := scan(ctx, directories)
		if !maps.Equal(previous, entries) && ctx.Err() == nil {
			_ = writeAgentGitCache(cachePath, entries)
		}

		return agentGitResultMsg{request: request, entries: entries}
	}
}

func (m *Model) handleAgentGit(msg agentGitResultMsg) tea.Cmd {
	m.finishAgentGit()

	if msg.request != m.agentGitRequest {
		return m.agentGitTickCommand()
	}

	m.agentGit = msg.entries

	selected := m.selectedSessionIdentity()

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)

	return m.agentGitTickCommand()
}

func (m *Model) invalidateAgentGit() {
	if !m.gitInFlight {
		return
	}

	m.agentGitRequest++
	m.stopAgentGit()
}

func (m *Model) stopAgentGit() {
	if m.agentGitCancel != nil {
		m.agentGitCancel()
		m.agentGitCancel = nil
	}
}

func (m *Model) finishAgentGit() {
	m.stopAgentGit()
	m.gitInFlight = false
}

func (m *Model) agentGitDirectories() []string {
	directories := make([]string, 0, len(m.sessions))

	for _, row := range m.sessions {
		if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge {
			continue
		}

		for _, agent := range row.Agents {
			if directory := agentGitDirectory(agent); directory != "" {
				directories = append(directories, directory)
			}
		}
	}

	slices.Sort(directories)

	return slices.Compact(directories)
}

func agentGitDirectory(agent session.AgentSession) string {
	if agent.CWD != "" {
		return agent.CWD
	}

	return agent.ProjectRoot
}

func (m *Model) applyCachedAgentGit(row *session.Session) {
	if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge {
		return
	}

	for index := range row.Agents {
		agent := &row.Agents[index]

		metadata, ok := m.agentGit[agentGitDirectory(*agent)]
		if !ok {
			continue
		}

		agent.GitBranch = metadata.GitBranch
		agent.Worktree = metadata.Worktree
		agent.GitChanges = metadata.GitChanges
		agent.LinkedWorktree = metadata.LinkedWorktree
		agent.GitAhead = metadata.GitAhead
		agent.GitBehind = metadata.GitBehind
		agent.GitTrackingKnown = metadata.GitTrackingKnown
	}
}
