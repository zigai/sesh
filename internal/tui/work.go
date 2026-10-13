package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

const (
	workSectionNeedsYou workSection = iota
	workSectionRunning
	workSectionYourTurn
	workSectionSleeping
	workSectionDone
	workSectionCount
)

const (
	workSectionIDPrefix = "work:"
	workDetailRows      = 6
	minWorkListRows     = 6
)

var errTaskWindowMissing = errors.New("its tagged window is not in tmux")

type workSection uint8

// TaskSource lists the recorded tasks with their live tmux location, and brings their records
// up to date: the pull request of each branch, and window titles from the titles agents report,
// keyed by task.
type TaskSource interface {
	LoadTasks(ctx context.Context) ([]task.Live, error)
	SyncTasks(ctx context.Context, titles map[string]string) (bool, error)
}

type taskSyncMsg struct {
	changed bool
}

// workHost is a machine whose tasks WORK lists: this one, or a connected remote host.
type workHost struct {
	origin   session.Origin
	identity session.RuntimeIdentity
	label    string
	tasks    []task.Live
}

// workTask is one task row of the WORK view: the agent row it renders as and the live session
// that owns its window, which actions and enter act on.
type workTask struct {
	live  task.Live
	agent session.AgentSession
	owner session.Session
	host  string
}

func (section workSection) label() string {
	switch section {
	case workSectionNeedsYou:
		return "needs you"
	case workSectionRunning:
		return "running"
	case workSectionYourTurn:
		return "your turn"
	case workSectionSleeping:
		return "sleeping"
	case workSectionDone:
		return "done"
	case workSectionCount:
		return ""
	default:
		return ""
	}
}

// workSessions groups the tasks of this machine and of every connected remote host into section
// rows whose agents are the task rows. Each host has its own row in a section, so a row tells
// which host its tasks and their actions belong to.
func (m *Model) workSessions() []session.Session {
	m.workTasks = make(map[string]workTask, len(m.tasks))
	hosts := m.workHosts()

	grouped := make([][][]session.AgentSession, len(hosts))
	for index, host := range hosts {
		grouped[index] = m.workHostSections(host)
	}

	rows := make([]session.Session, 0, workSectionCount)

	for section := range workSectionCount {
		for index, host := range hosts {
			if agents := grouped[index][section]; len(agents) > 0 {
				rows = append(rows, workSectionRow(section, host, agents))
			}
		}
	}

	return rows
}

// workHosts lists this machine and the connected remote hosts that report tasks.
func (m *Model) workHosts() []workHost {
	local := session.RuntimeIdentity{
		ConnectionID: "", ConnectionLabel: "", ConfigDigest: "", PeerID: "", StreamInstance: "", TmuxServerSocket: "",
		TmuxServerPID: 0, TmuxServerStartedAt: time.Time{}, TmuxSessionCreatedAt: time.Time{},
	}

	hosts := []workHost{{origin: session.OriginLocal, identity: local, label: "", tasks: m.tasks}}
	if !remoteSessionsEnabled(m.config, m.deps) {
		return hosts
	}

	for _, connection := range m.remoteCatalog.Connections {
		if !connection.Connected || len(connection.Tasks) == 0 {
			continue
		}

		label := connection.Label
		if label == "" {
			label = connection.ID
		}

		hosts = append(hosts, workHost{
			origin: session.OriginSSH,
			identity: session.RuntimeIdentity{
				ConnectionID: connection.ID, ConnectionLabel: connection.Label, ConfigDigest: connection.ConfigDigest,
				PeerID: connection.PeerID, StreamInstance: connection.StreamInstance, TmuxServerSocket: "",
				TmuxServerPID: 0, TmuxServerStartedAt: time.Time{}, TmuxSessionCreatedAt: time.Time{},
			},
			label: label,
			tasks: connection.Tasks,
		})
	}

	return hosts
}

func (m *Model) workHostSections(host workHost) [][]session.AgentSession {
	agents := m.hostAgentsByPane(host)
	owners := m.hostSessionsByID(host)
	sections := make([][]session.AgentSession, workSectionCount)

	for _, live := range host.tasks {
		if (live.Record.State == task.StateCreating || live.Record.State == task.StateFinishing) && !live.Stalled {
			continue
		}

		agent := taskAgent(live, agents)
		if metadata, ok := m.agentGit[agentGitDirectory(agent)]; ok && host.origin == session.OriginLocal {
			applyAgentGitMetadata(&agent, metadata)
		}

		section := taskSection(live.Record, agent)
		sections[section] = append(sections[section], agent)
		m.workTasks[workKey(host.identity.ConnectionID, agent.PaneID)] = workTask{
			live: live, agent: agent, owner: owners[string(live.SessionID)], host: host.label,
		}
	}

	return sections
}

func workSectionRow(section workSection, host workHost, agents []session.AgentSession) session.Session {
	var row session.Session

	row.ID = workSectionIDPrefix + section.label()
	row.Name = section.label()

	if host.origin != session.OriginLocal {
		row.ID += "@" + host.identity.ConnectionID
		row.Name += " · " + host.label
	}

	row.Kind = session.KindLive
	row.Origin = host.origin
	row.RuntimeIdentity = host.identity
	row.Agents = agents
	row.AgentTotal = len(agents)

	return row
}

// workKey identifies a task row: pane IDs are only unique within one host.
func workKey(connection, paneID string) string {
	return connection + "\x00" + paneID
}

// syncTasksCommand brings the task records up to date once per picker, after tasks first load,
// and reloads them when anything changed.
func (m *Model) syncTasksCommand() tea.Cmd {
	if m.deps.Tasks == nil || m.tasksSynced || len(m.tasks) == 0 {
		return nil
	}

	m.tasksSynced = true

	ctx := m.operationContext()
	source := m.deps.Tasks
	titles := m.taskTitles()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		changed, err := source.SyncTasks(ctx, titles)

		return taskSyncMsg{changed: changed && err == nil}
	}
}

func (m *Model) handleTaskSync(msg taskSyncMsg) tea.Cmd {
	if !msg.changed {
		return nil
	}

	return m.refreshSessions()
}

// taskTitles are the titles AHT reports for the agents running in task windows.
func (m *Model) taskTitles() map[string]string {
	agents := m.hostAgentsByPane(m.workHosts()[0])
	titles := make(map[string]string, len(m.tasks))

	for _, live := range m.tasks {
		if agent, found := agents[string(live.AgentPane)]; found && live.AgentPane != "" && agent.Task != "" {
			titles[live.Record.ID] = agent.Task
		}
	}

	return titles
}

// workTaskAt returns the task of a row in the WORK view.
func (m *Model) workTaskAt(row popupListRow) (workTask, bool) {
	if m.displayMode != displayModeWork || !row.isAgent() || row.sessionIndex >= len(m.filtered) {
		return emptyWorkTask(), false
	}

	section := m.filtered[row.sessionIndex]
	if row.agentIndex >= len(section.Agents) {
		return emptyWorkTask(), false
	}

	found, ok := m.workTasks[workKey(section.RuntimeIdentity.ConnectionID, section.Agents[row.agentIndex].PaneID)]

	return found, ok
}

func emptyWorkTask() workTask {
	var empty workTask

	return empty
}

func (m *Model) hostAgentsByPane(host workHost) map[string]session.AgentSession {
	agents := make(map[string]session.AgentSession)

	for _, row := range m.sessions {
		if !host.owns(row) {
			continue
		}

		for _, agent := range row.Agents {
			if agent.PaneID != "" {
				agents[agent.PaneID] = agent
			}
		}
	}

	return agents
}

func (m *Model) hostSessionsByID(host workHost) map[string]session.Session {
	owners := make(map[string]session.Session, len(m.sessions))

	for _, row := range m.sessions {
		if host.owns(row) {
			owners[row.ID] = row
		}
	}

	return owners
}

// owns reports whether a live session row is on the host.
func (host workHost) owns(row session.Session) bool {
	return row.Origin == host.origin && !row.Saved() && row.RuntimeIdentity.ConnectionID == host.identity.ConnectionID
}

// taskAgent is the agent row of a task: the agent AHT reports in its agent pane, else a row
// describing the task itself, for a task whose agent is asleep, exited or never started.
func taskAgent(live task.Live, agents map[string]session.AgentSession) session.AgentSession {
	record := live.Record

	agent, running := agents[string(live.AgentPane)]
	if !running || live.AgentPane == "" {
		agent = emptyAgentSession()
		agent.PaneID = string(live.AgentPane)
		agent.Harness = record.Harness
		agent.HarnessSession = live.WindowName
		agent.CWD = record.Directory
		agent.Status = session.AgentStatusIdle
		agent.StatusSince = time.Unix(record.CreatedAt, 0)

		if !live.Present {
			agent.Status = session.AgentStatusUnknown
			agent.HarnessSession = record.Title
		}
	}

	if agent.PaneID == "" {
		agent.PaneID = workSectionIDPrefix + record.ID
	}

	agent.ProjectRoot = record.Project
	if record.Kind == task.KindLoose {
		agent.ProjectRoot = filepath.Dir(record.Directory)
	}

	if agent.GitBranch == "" {
		agent.GitBranch = record.Branch
	}

	switch {
	case record.Done():
		agent.Status = session.AgentStatusDone
	case record.State == task.StateSleeping || record.State == task.StateWaking:
		agent.Status = session.AgentStatusSleeping
	case record.State == task.StateFailed || live.Stalled:
		agent.Status = session.AgentStatusFailed
	}

	return agent
}

func taskSection(record task.Record, agent session.AgentSession) workSection {
	if record.State == task.StateWaking {
		return workSectionNeedsYou
	}

	switch agent.Status.DisplayStatus() {
	case session.AgentStatusDone:
		return workSectionDone
	case session.AgentStatusSleeping:
		return workSectionSleeping
	case session.AgentStatusWaiting, session.AgentStatusFailed:
		return workSectionNeedsYou
	case session.AgentStatusRunning:
		return workSectionRunning
	case session.AgentStatusIdle, session.AgentStatusInterrupted, session.AgentStatusUnknown:
		return workSectionYourTurn
	default:
		return workSectionYourTurn
	}
}

func (m *Model) handleTaskEnter(selected popupListRow) func() (tea.Model, tea.Cmd) {
	found, ok := m.workTaskAt(selected)
	if !ok {
		return nil
	}

	if found.owner.ID == "" || found.live.AgentPane == "" {
		m.setErrorStatusf("Task %s has no window: %v", found.live.Record.ID, errTaskWindowMissing)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	agent := found.agent
	agent.PaneID = string(found.live.AgentPane)

	return func() (tea.Model, tea.Cmd) {
		return m, m.runAgentSessionAction(found.owner, agent)
	}
}

// recordedTaskPR is the pull request the task's ledger records for a task row, which outlives
// the open-PR lookup once the PR is merged or closed and is all that is known for a remote task.
func (m *Model) recordedTaskPR(row session.Session, paneID string) int {
	if m.displayMode != displayModeWork {
		return 0
	}

	return m.workTasks[workKey(row.RuntimeIdentity.ConnectionID, paneID)].live.Record.PR.Number
}

// footerActions lists the actions the footer offers. In WORK the selected task's own actions come
// first, and only the one that fits its state: sleep for an awake task, wake for a sleeping one.
func (m *Model) footerActions(keys actionKeyMap) []boundAction {
	available := m.availableActions(keys)
	if m.displayMode != displayModeWork {
		return available
	}

	found, ok := m.workTaskAt(m.selectedListRow())

	tasks := make([]boundAction, 0, len(available))
	others := make([]boundAction, 0, len(available))

	for _, bound := range available {
		taskAction, fits := taskActionFits(bound.binding.Action, found.live)

		switch {
		case !taskAction:
			others = append(others, bound)
		case ok && fits:
			tasks = append(tasks, bound)
		}
	}

	return append(tasks, others...)
}

// taskActionFits reports whether an action is one of a task's own and, if so, whether it fits a
// task in the given state.
func taskActionFits(id action.ID, live task.Live) (bool, bool) {
	state := live.Record.State

	switch id {
	case action.TaskSleep.ID:
		return true, state == task.StateActive
	case action.TaskWake.ID:
		return true, state == task.StateSleeping || state == task.StateWaking
	case action.TaskOpen.ID:
		return true, !live.Present && !live.Stalled && state != task.StateCreating && state != task.StateFinishing
	case action.TaskFinish.ID, action.TaskRun.ID:
		return true, true
	default:
		return false, false
	}
}

// renderWorkDetail draws the rows under the WORK list that describe the selected task.
func (m *Model) renderWorkDetail(width, rows int) []string {
	if rows == 0 {
		return nil
	}

	chrome := NewChrome(m.config)
	lines := make([]string, 0, rows)
	lines = append(lines, m.detailSeparator(width))

	found, ok := m.workTaskAt(m.selectedListRow())
	if !ok {
		for len(lines) < rows {
			lines = append(lines, chrome.Blank(width))
		}

		return lines
	}

	record := found.live.Record

	branch := record.Branch
	if record.Base != "" {
		branch += " ← " + record.Base
	}

	location := "no window"
	if found.live.Present {
		location = fmt.Sprintf("%s:%d %s", found.live.SessionName, found.live.WindowIndex, found.live.WindowName)
	}

	directory := session.ShortenHomePath(record.Directory)
	if found.host != "" {
		location, directory = found.host+" "+location, record.Directory
	}

	fields := [][2]string{
		{"branch", branch},
		{"dir", directory},
		{"tmux", location},
		{"state", taskStateDetail(record)},
		{"runs", taskRunsDetail(record.Runs)},
	}

	for _, field := range fields[:min(len(fields), rows-1)] {
		if field[1] == "" {
			field[1] = "-"
		}

		lines = append(lines, chrome.Pad(chrome.Muted(fmt.Sprintf("%-7s", field[0]))+field[1], width))
	}

	return lines
}

func taskStateDetail(record task.Record) string {
	parts := []string{string(record.State)}
	if record.PR.Number != 0 {
		parts = append(parts, fmt.Sprintf("PR #%d %s", record.PR.Number, record.PR.State))
	}

	if record.Done() {
		parts = append(parts, "ready to finish")
	}

	return strings.Join(parts, " · ")
}

func taskRunsDetail(runs []task.Run) string {
	parts := make([]string, 0, len(runs))

	for _, run := range runs {
		switch run.Status {
		case task.RunFailed:
			parts = append(parts, fmt.Sprintf("%s failed (exit %d)", run.Action, run.ExitCode))
		case task.RunOK, task.RunRunning:
			parts = append(parts, run.Action+" "+string(run.Status))
		}
	}

	return strings.Join(parts, " · ")
}

func (m *Model) detailSeparator(width int) string {
	line := strings.Repeat("─", max(width, 0))
	if defaultBorderTheme(m.config) {
		return ansiDefaultMuted + line + ansiReset
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Border)).Render(line)
}
