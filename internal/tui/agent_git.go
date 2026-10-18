package tui

import (
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

type agentGitTickMsg struct{}

type agentGitResultMsg struct {
	request uint64
	entries map[string]agentGitCacheEntry
}

func (m *Model) agentGitEnabled() bool {
	definition, _ := providers.DefinitionFor(providers.Git)

	return m.mode == modePopup && agentSummariesEnabled(m.config, m.deps) &&
		m.sessionFormats.agentRowsUseAny(definition.Fields...)
}

func (m *Model) agentGitTickCommand() tea.Cmd {
	if !m.agentGitEnabled() {
		return nil
	}

	definition, _ := providers.DefinitionFor(providers.Git)

	return tea.Tick(definition.Cadence.Interval, func(time.Time) tea.Msg { return agentGitTickMsg{} })
}

func (m *Model) loadAgentGitCommand() tea.Cmd {
	if !m.agentGitEnabled() || m.providerRunner.InFlight(providers.Git) {
		return m.agentGitTickCommand()
	}

	directories := m.agentGitDirectories()
	if len(directories) == 0 {
		return m.agentGitTickCommand()
	}

	ctx, request, ok := m.providerRunner.Begin(m.operationContext(), providers.Git)
	if !ok {
		return m.agentGitTickCommand()
	}

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
	if !m.providerRunner.Complete(providers.Git, msg.request, m.gitProviderRows(msg.entries)) {
		return m.agentGitTickCommand()
	}

	m.agentGit = msg.entries
	m.providerRunner.Invalidate(providers.GH)

	selected := m.selectedSessionIdentity()

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)

	return tea.Batch(m.agentGitTickCommand(), m.loadGHCommand())
}

func (m *Model) invalidateAgentGit() {
	if !m.providerRunner.InFlight(providers.Git) {
		return
	}

	m.providerRunner.Invalidate(providers.Git)
}

func (m *Model) gitProviderRows(entries map[string]agentGitCacheEntry) providers.Rows {
	definition, _ := providers.DefinitionFor(providers.Git)
	rows := make(providers.Rows)

	for _, row := range m.sessions {
		if row.Origin == session.OriginSSH || row.Origin == session.OriginBridge {
			continue
		}

		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			if metadata, ok := entries[agentGitDirectory(agent)]; ok {
				applyAgentGitMetadata(&agent, metadata)
			}

			values := sessionFormatValues{
				model: m, row: row, agent: agent, hasAgent: true, isLastAgent: false,
				isActivePane: false, ageWidth: 0, contextWidth: 0, now: time.Time{},
			}

			fields := make(map[string]layout.Cell, len(definition.Fields))
			for _, field := range definition.Fields {
				value := values.ResolveSessionFormatField(field)

				state := layout.Absent
				if value != "" {
					state = layout.Present
				}

				fields[field] = layout.Cell{Text: value, Role: "", Variants: nil, State: state}
			}

			rows[agent.PaneID] = fields
		}
	}

	return rows
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

	for _, live := range m.tasks {
		if live.Record.Directory != "" {
			directories = append(directories, live.Record.Directory)
		}
	}

	directories = append(directories, m.workDirectories()...)

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

		applyAgentGitMetadata(agent, metadata)
	}
}

func applyAgentGitMetadata(agent *session.AgentSession, metadata agentGitCacheEntry) {
	agent.GitBranch = metadata.GitBranch
	agent.Worktree = metadata.Worktree
	agent.GitRepo = metadata.GitRepo
	agent.GitHasRemote = metadata.GitHasRemote
	agent.GitChanges = metadata.GitChanges
	agent.LinkedWorktree = metadata.LinkedWorktree
	agent.GitAhead = metadata.GitAhead
	agent.GitBehind = metadata.GitBehind
	agent.GitTrackingKnown = metadata.GitTrackingKnown
	agent.GitStash = metadata.GitStash
	agent.GitConflicts = metadata.GitConflicts
	agent.GitDirty = metadata.GitDirty
	agent.GitUnmerged = metadata.GitUnmerged
}
