package tui

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui/providers"
)

const (
	inboxGroupIDPrefix   = "inbox:"
	inboxOfflineIDPrefix = inboxGroupIDPrefix + "offline:"
	inboxDetailRows      = 7
	minInboxListRows     = 6
	taskRunPollInterval  = time.Second
)

// TaskSource lists the recorded tasks with their live tmux location, and brings their records
// up to date: the pull request of each branch, and window titles from the titles agents report,
// keyed by task.
type TaskSource interface {
	LoadTasks(ctx context.Context) ([]task.Live, error)
	SyncTasks(ctx context.Context, titles map[string]string) (bool, error)
}

type taskRunTickMsg struct{}

type taskSyncMsg struct {
	changed bool
}

// inboxHost is a machine whose agents and tasks the inbox lists: this one, or a connected remote
// host.
type inboxHost struct {
	origin   session.Origin
	identity session.RuntimeIdentity
	label    string
	tasks    []task.Live
}

// inboxItem is one row of the inbox: an agent, or a task whose agent is not running, with the live
// session that owns its pane, the machine it runs on and the section it is listed in.
type inboxItem struct {
	agent   session.AgentSession
	owner   session.Session
	host    inboxHost
	live    task.Live
	task    bool
	section string
}

// inboxSessions lists the agents and tasks of this machine and of every connected remote host as
// group rows whose agents are the inbox rows, grouped by section or by project.
func (m *Model) inboxSessions() []session.Session {
	m.inboxItems = make(map[string]inboxItem)

	var items []inboxItem
	for _, host := range m.inboxHosts() {
		items = append(items, m.hostInboxItems(host)...)
	}

	shown := m.config.Inbox.Sections
	items = slices.DeleteFunc(items, func(item inboxItem) bool { return !slices.Contains(shown, item.section) })

	for _, item := range items {
		m.inboxItems[inboxKey(item.agent.Host, item.agent.PaneID)] = item
	}

	slices.SortStableFunc(items, func(left, right inboxItem) int { return m.compareInboxItems(left, right) })

	rows := m.inboxStateRows(items)
	if m.inboxGroup == config.InboxGroupProject {
		rows = m.inboxProjectRows(items)
	}

	return append(rows, m.inboxOfflineRows()...)
}

// inboxOfflineRows are one heading for each configured remote host the picker cannot reach.
func (m *Model) inboxOfflineRows() []session.Session {
	if !remoteSessionsEnabled(m.config, m.deps) {
		return nil
	}

	var rows []session.Session

	for _, connection := range m.remoteCatalog.Connections {
		if remoteConnectionStatus(connection, m.config.Remote.StaleAfter, time.Now()) != session.RemoteStatusUnavailable {
			continue
		}

		label := connection.Label
		if label == "" {
			label = connection.ID
		}

		rows = append(rows, inboxGroupRow("offline:"+connection.ID, label+" offline", nil))
	}

	return rows
}

func (m *Model) inboxStateRows(items []inboxItem) []session.Session {
	rows := make([]session.Session, 0, len(m.config.Inbox.Sections))

	for _, section := range m.config.Inbox.Sections {
		var agents []session.AgentSession

		for _, item := range items {
			if item.section == section {
				agents = append(agents, item.agent)
			}
		}

		if len(agents) > 0 {
			rows = append(rows, inboxGroupRow(section, m.config.Inbox.Label(section), agents))
		}
	}

	return rows
}

// inboxProjectRows groups the rows by project and host. A group whose most urgent row comes first
// in the section order is listed first.
func (m *Model) inboxProjectRows(items []inboxItem) []session.Session {
	var keys []string

	groups := make(map[string][]session.AgentSession)
	names := make(map[string]string)

	for _, item := range items {
		project := inboxProject(item)
		key := item.agent.Host + "\x00" + project

		if _, seen := groups[key]; !seen {
			keys = append(keys, key)
			names[key] = pathName(project)

			if item.host.label != "" {
				names[key] += "@" + item.host.label
			}
		}

		groups[key] = append(groups[key], item.agent)
	}

	rows := make([]session.Session, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, inboxGroupRow(key, names[key], groups[key]))
	}

	return rows
}

func inboxGroupRow(id, name string, agents []session.AgentSession) session.Session {
	var row session.Session

	row.ID = inboxGroupIDPrefix + id
	row.Name = name
	row.Kind = session.KindLive
	row.Origin = session.OriginLocal
	row.Agents = agents
	row.AgentTotal = len(agents)

	return row
}

// inboxProject is the project an inbox row belongs to: its task's project, else the agent's.
func inboxProject(item inboxItem) string {
	switch {
	case item.task && item.live.Record.Kind == task.KindProject:
		return item.live.Record.Project
	case item.agent.ProjectRoot != "":
		return item.agent.ProjectRoot
	default:
		return item.agent.CWD
	}
}

// compareInboxItems orders rows by the configured section order, then the most recent change
// first.
func (m *Model) compareInboxItems(left, right inboxItem) int {
	sections := m.config.Inbox.Sections
	if order := cmp.Compare(slices.Index(sections, left.section), slices.Index(sections, right.section)); order != 0 {
		return order
	}

	return right.agent.StatusSince.Compare(left.agent.StatusSince)
}

// inboxKey identifies an inbox row: pane IDs are only unique within one host.
func inboxKey(host, paneID string) string {
	return host + "\x00" + paneID
}

// inboxHosts lists this machine and the connected remote hosts.
func (m *Model) inboxHosts() []inboxHost {
	local := session.RuntimeIdentity{
		ConnectionID: "", ConnectionLabel: "", ConfigDigest: "", PeerID: "", StreamInstance: "", TmuxServerSocket: "",
		TmuxServerPID: 0, TmuxServerStartedAt: time.Time{}, TmuxSessionCreatedAt: time.Time{},
	}

	hosts := []inboxHost{{origin: session.OriginLocal, identity: local, label: "", tasks: m.tasks}}
	if !remoteSessionsEnabled(m.config, m.deps) {
		return hosts
	}

	for _, connection := range m.remoteCatalog.Connections {
		if !connection.Connected {
			continue
		}

		label := connection.Label
		if label == "" {
			label = connection.ID
		}

		hosts = append(hosts, inboxHost{
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

// hostInboxItems lists every agent of a host, and the tasks of the host whose agent is not
// running.
func (m *Model) hostInboxItems(host inboxHost) []inboxItem {
	agents := m.hostAgentsByPane(host)

	return append(m.hostAgentItems(host, agents), m.hostTaskItems(host, agents)...)
}

// hostAgentItems lists the agents of a host. An agent in a task's agent pane is that task's row.
func (m *Model) hostAgentItems(host inboxHost, agents map[string]session.AgentSession) []inboxItem {
	tasks := make(map[string]task.Live, len(host.tasks))

	for _, live := range host.tasks {
		if listedTask(live) && live.AgentPane != "" {
			tasks[string(live.AgentPane)] = live
		}
	}

	var items []inboxItem

	for _, row := range m.sessions {
		if !host.owns(row) {
			continue
		}

		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			live, isTask := tasks[agent.PaneID]
			if isTask {
				agent = taskAgent(live, agents)
			}

			items = append(items, m.newInboxItem(host, row, agent, live, isTask))
		}
	}

	return items
}

// hostTaskItems lists the tasks of a host whose agent is not running. A task with no window is
// listed only while it failed, has work to merge, is asleep, or waits to be finished after its
// pull request ended.
func (m *Model) hostTaskItems(host inboxHost, agents map[string]session.AgentSession) []inboxItem {
	owners := m.hostSessionsByID(host)

	var items []inboxItem

	for _, live := range host.tasks {
		if _, running := agents[string(live.AgentPane)]; !listedTask(live) || (running && live.AgentPane != "") {
			continue
		}

		item := m.newInboxItem(host, owners[string(live.SessionID)], taskAgent(live, agents), live, true)
		if live.Present || needsWindowlessRow(item) {
			items = append(items, item)
		}
	}

	return items
}

func needsWindowlessRow(item inboxItem) bool {
	return item.agent.Status == session.AgentStatusFailed || item.section == config.InboxUnmerged ||
		item.section == config.InboxAsleep || item.live.Record.PREnded()
}

// listedTask reports whether the inbox lists a task: one being created or finished appears only
// once the sesh process doing it is gone.
func listedTask(live task.Live) bool {
	return (live.Record.State != task.StateCreating && live.Record.State != task.StateFinishing) || live.Stalled
}

func (m *Model) newInboxItem(host inboxHost, owner session.Session, agent session.AgentSession, live task.Live, isTask bool) inboxItem {
	if metadata, ok := m.agentGit[agentGitDirectory(agent)]; ok && host.origin == session.OriginLocal {
		applyAgentGitMetadata(&agent, metadata)
	}

	agent.Host = host.identity.ConnectionID

	item := inboxItem{agent: agent, owner: owner, host: host, live: live, task: isTask, section: ""}
	item.section = m.inboxSection(item)

	return item
}

// inboxSection places a row: a sleeping task is asleep; otherwise the agent's state decides, and
// an idle agent the user has seen is unmerged while its branch has work not on the default branch
// or an open pull request.
func (m *Model) inboxSection(item inboxItem) string {
	record := item.live.Record

	switch {
	case item.task && (record.State == task.StateSleeping || record.State == task.StateWaking):
		return config.InboxAsleep
	case m.snoozed(item):
		return config.InboxSnoozed
	}

	if section, decided := statusSection(item.agent.Status.DisplayStatus()); decided {
		return section
	}

	if (item.owner.ID != "" && m.agentIdleUnread(item.owner, item.agent)) || m.inboxDoneByMark(item) {
		return config.InboxDone
	}

	if m.unmergedItem(item) {
		return config.InboxUnmerged
	}

	return config.InboxIdle
}

// statusSection is the section an agent's status alone decides, if any.
func statusSection(status session.AgentStatus) (string, bool) {
	switch status {
	case session.AgentStatusWaiting:
		return config.InboxWaiting, true
	case session.AgentStatusFailed:
		return config.InboxDone, true
	case session.AgentStatusRunning:
		return config.InboxRunning, true
	case session.AgentStatusMerged:
		return config.InboxIdle, true
	case session.AgentStatusIdle, session.AgentStatusInterrupted, session.AgentStatusUnknown, session.AgentStatusSleeping:
	}

	return "", false
}

// unmergedItem reports whether a row has work to merge: commits its checkout has that the default
// branch lacks, or an open pull request.
func (m *Model) unmergedItem(item inboxItem) bool {
	if item.agent.GitUnmerged > 0 || (item.task && item.live.Record.PR.State == task.PROpen) {
		return true
	}

	if item.agent.Host != "" || item.agent.PaneID == "" {
		return false
	}

	return m.inboxPR(item) != ""
}

// inboxPR is the pull request of a row: the open one gh lists for its branch, else the one its
// task records with that pull request's state when it is no longer open.
func (m *Model) inboxPR(item inboxItem) string {
	if item.agent.Host == "" && item.agent.PaneID != "" {
		if cell := m.providerRunner.Cell(providers.GH, item.agent.PaneID, display.FieldAgentPR); cell.Text != "" {
			return cell.Text
		}
	}

	pr := item.live.Record.PR
	if !item.task || pr.Number == 0 {
		return ""
	}

	if pr.State == task.PROpen {
		return "#" + strconv.Itoa(pr.Number)
	}

	return "#" + strconv.Itoa(pr.Number) + " " + string(pr.State)
}

// taskAgent is the agent row of a task, named after the task: the agent AHT reports in its agent
// pane, else a row describing the task itself, for a task whose agent is asleep, exited or never
// started.
func taskAgent(live task.Live, agents map[string]session.AgentSession) session.AgentSession {
	record := live.Record

	agent, running := agents[string(live.AgentPane)]
	if !running || live.AgentPane == "" {
		agent = agentlessTaskRow(live)
	}

	if agent.PaneID == "" {
		agent.PaneID = inboxGroupIDPrefix + record.ID
	}

	agent.ProjectRoot = record.Project
	if record.Kind == task.KindLoose {
		agent.ProjectRoot = filepath.Dir(record.Directory)
	}

	if agent.GitBranch == "" {
		agent.GitBranch = record.Branch
	}

	agent.HarnessSession = cmp.Or(live.WindowName, record.Title, agent.HarnessSession)
	agent.Status = taskStatus(live, agent.Status)

	return agent
}

// agentlessTaskRow describes a task with no agent running in its agent pane.
func agentlessTaskRow(live task.Live) session.AgentSession {
	record := live.Record

	agent := emptyAgentSession()
	agent.PaneID = string(live.AgentPane)
	agent.Harness = record.Harness
	agent.CWD = record.Directory
	agent.Status = session.AgentStatusIdle
	agent.StatusSince = time.Unix(record.CreatedAt, 0)
	agent.InitialIdle = true

	if !live.Present {
		agent.Status = session.AgentStatusUnknown
	}

	return agent
}

// taskStatus is the status a task row shows: the task's own state wins over its agent's when the
// task sleeps or failed, and a merged pull request over an agent that is not busy.
func taskStatus(live task.Live, status session.AgentStatus) session.AgentStatus {
	record := live.Record

	switch {
	case record.State == task.StateSleeping || record.State == task.StateWaking:
		return session.AgentStatusSleeping
	case record.State == task.StateFailed || live.Stalled:
		return session.AgentStatusFailed
	case record.PREnded() && status != session.AgentStatusRunning && status != session.AgentStatusWaiting:
		return session.AgentStatusMerged
	default:
		return status
	}
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

// taskRunTickCommand reloads the picker after a moment while a task run, such as a merge, can
// still change the tasks: one the picker just started, one in progress, or a finish under way.
func (m *Model) taskRunTickCommand(started bool) tea.Cmd {
	if m.taskRunTickPending || (!started && !m.taskRunInProgress()) {
		return nil
	}

	m.taskRunTickPending = true

	return tea.Tick(taskRunPollInterval, func(time.Time) tea.Msg { return taskRunTickMsg{} })
}

func (m *Model) taskRunInProgress() bool {
	return slices.ContainsFunc(m.tasks, func(live task.Live) bool {
		runs := live.Record.Runs
		finishing := live.Record.State == task.StateFinishing && !live.Stalled

		return finishing || (len(runs) > 0 && runs[len(runs)-1].Status == task.RunRunning)
	})
}

func (m *Model) handleTaskSync(msg taskSyncMsg) tea.Cmd {
	if !msg.changed {
		return nil
	}

	return m.refreshSessions()
}

// taskTitles are the titles AHT reports for the agents running in task windows.
func (m *Model) taskTitles() map[string]string {
	agents := m.hostAgentsByPane(m.inboxHosts()[0])
	titles := make(map[string]string, len(m.tasks))

	for _, live := range m.tasks {
		if agent, found := agents[string(live.AgentPane)]; found && live.AgentPane != "" && agent.Task != "" {
			titles[live.Record.ID] = agent.Task
		}
	}

	return titles
}

// inboxItemAt returns the inbox row at a list row.
func (m *Model) inboxItemAt(row popupListRow) (inboxItem, bool) {
	if m.displayMode != displayModeInbox || !row.isAgent() || row.sessionIndex >= len(m.filtered) {
		return emptyInboxItem(), false
	}

	group := m.filtered[row.sessionIndex]
	if row.agentIndex >= len(group.Agents) {
		return emptyInboxItem(), false
	}

	agent := group.Agents[row.agentIndex]
	found, ok := m.inboxItems[inboxKey(agent.Host, agent.PaneID)]

	return found, ok
}

func (m *Model) selectedInboxItem() (inboxItem, bool) {
	return m.inboxItemAt(m.selectedListRow())
}

func emptyInboxItem() inboxItem {
	var empty inboxItem

	return empty
}

func (m *Model) hostAgentsByPane(host inboxHost) map[string]session.AgentSession {
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

func (m *Model) hostSessionsByID(host inboxHost) map[string]session.Session {
	owners := make(map[string]session.Session, len(m.sessions))

	for _, row := range m.sessions {
		if host.owns(row) {
			owners[row.ID] = row
		}
	}

	return owners
}

// owns reports whether a live session row is on the host.
func (host inboxHost) owns(row session.Session) bool {
	return row.Origin == host.origin && !row.Saved() && row.RuntimeIdentity.ConnectionID == host.identity.ConnectionID
}

// hostLabel names the machine of a remote connection, empty for this machine.
func (m *Model) hostLabel(connection string) string {
	if connection == "" {
		return ""
	}

	for _, configured := range m.remoteCatalog.Connections {
		if configured.ID == connection && configured.Label != "" {
			return configured.Label
		}
	}

	return connection
}

// handleInboxEnter goes to the selected row's agent pane; a task with no window is opened again.
func (m *Model) handleInboxEnter(selected popupListRow) func() (tea.Model, tea.Cmd) {
	item, ok := m.inboxItemAt(selected)
	if !ok {
		return nil
	}

	if item.task && !item.live.Present {
		return m.invokeAction(action.TaskOpen.ID, action.Values{})
	}

	agent := item.agent
	if item.task {
		agent.PaneID = string(item.live.AgentPane)
	}

	if item.host.origin == session.OriginSSH {
		return m.attachRemote(item.owner, agent.PaneID)
	}

	return func() (tea.Model, tea.Cmd) {
		return m, m.runAgentSessionAction(item.owner, agent)
	}
}

// recordedTaskPR is the pull request the task's ledger records for an inbox row, which outlives
// the open-PR lookup once the PR is merged or closed and is all that is known for a remote task.
func (m *Model) recordedTaskPR(agent session.AgentSession) string {
	if m.displayMode != displayModeInbox {
		return ""
	}

	item, ok := m.inboxItems[inboxKey(agent.Host, agent.PaneID)]
	if !ok || !item.task || item.live.Record.PR.Number == 0 {
		return ""
	}

	pr := item.live.Record.PR
	if pr.State == task.PROpen {
		return "#" + strconv.Itoa(pr.Number)
	}

	return "#" + strconv.Itoa(pr.Number) + " " + string(pr.State)
}

// footerActions lists the actions the footer offers. In the inbox the selected task's own actions
// come first, and only the one that fits its state: sleep for an awake task, wake for a sleeping
// one.
func (m *Model) footerActions(keys actionKeyMap) []boundAction {
	available := m.availableActions(keys)
	if m.displayMode != displayModeInbox {
		return available
	}

	item, ok := m.selectedInboxItem()

	tasks := make([]boundAction, 0, len(available))
	others := make([]boundAction, 0, len(available))

	for _, bound := range available {
		taskAction, fits := taskActionFits(bound.binding.Action, item.live)

		switch {
		case !taskAction:
			others = append(others, bound)
		case ok && item.task && fits:
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

// renderInboxDetail draws the rows under the inbox list that describe the selected row.
func (m *Model) renderInboxDetail(width, rows int) []string {
	if rows == 0 {
		return nil
	}

	chrome := NewChrome(m.config)
	lines := make([]string, 0, rows)
	lines = append(lines, m.detailSeparator(width))

	item, ok := m.selectedInboxItem()
	if !ok {
		for len(lines) < rows {
			lines = append(lines, chrome.Blank(width))
		}

		return lines
	}

	fields := m.inboxDetailFields(item)
	for _, field := range fields[:min(len(fields), rows-1)] {
		if field[1] == "" {
			field[1] = "-"
		}

		lines = append(lines, chrome.Pad(chrome.Muted(fmt.Sprintf("%-9s", field[0]))+field[1], width))
	}

	for len(lines) < rows {
		lines = append(lines, chrome.Blank(width))
	}

	return lines
}

func (m *Model) inboxDetailFields(item inboxItem) [][2]string {
	record := item.live.Record

	branch := item.agent.GitBranch
	if item.task && record.Base != "" {
		branch += " ← " + record.Base
	}

	location := "no window"
	if item.owner.ID != "" {
		location = item.owner.Name
		if item.task && item.live.Present {
			location = fmt.Sprintf("%s:%d %s", item.live.SessionName, item.live.WindowIndex, item.live.WindowName)
		}
	}

	directory := session.ShortenHomePath(item.agent.CWD)
	if item.host.label != "" {
		location, directory = item.host.label+" "+location, item.agent.CWD
	}

	fields := [][2]string{
		{"branch", branch},
		{"dir", directory},
		{"tmux", location},
		{"state", m.inboxStateDetail(item)},
	}

	if item.task {
		fields = append(fields, [2]string{"runs", taskRunsDetail(record.Runs)})
	}

	if evidence := m.evidenceOf(item); len(evidence) > 0 {
		fields = append(fields, [2]string{"evidence", evidenceDetail(evidence, item.agent.CWD)})
	}

	return fields
}

func (m *Model) inboxStateDetail(item inboxItem) string {
	parts := []string{m.config.Inbox.Label(item.section)}

	if snooze, ok := m.inbox.Find(conversationKey(item.agent)); ok && item.section == config.InboxSnoozed {
		parts[0] += " " + snoozeLabel(snooze.Until)
	}

	if item.task && item.live.Record.State != task.StateActive {
		parts = append(parts, "task "+string(item.live.Record.State))
	}

	if item.agent.GitUnmerged > 0 {
		parts = append(parts, plural(item.agent.GitUnmerged, "commit")+" not on the default branch")
	}

	if pr := m.inboxPR(item); pr != "" {
		parts = append(parts, "PR "+pr)
	}

	if item.task && item.live.Record.PREnded() {
		parts = append(parts, "ready to finish")
	}

	return strings.Join(parts, " · ")
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(count) + " " + noun + "s"
}

func taskRunsDetail(runs []task.Run) string {
	parts := make([]string, 0, len(runs))

	for _, run := range runs {
		switch run.Status {
		case task.RunFailed:
			parts = append(parts, runFailure(run))
		case task.RunOK, task.RunRunning:
			parts = append(parts, run.Action+" "+string(run.Status))
		}
	}

	return strings.Join(parts, " · ")
}

func runFailure(run task.Run) string {
	if run.Action == task.MergeAction {
		switch run.ExitCode {
		case task.MergeExitConflict:
			return "merge conflict"
		case task.MergeExitCheck:
			return "merge check failed"
		case task.MergeExitDirty:
			return "merge refused: uncommitted changes"
		}
	}

	return fmt.Sprintf("%s failed (exit %d)", run.Action, run.ExitCode)
}

func (m *Model) detailSeparator(width int) string {
	line := strings.Repeat("─", max(width, 0))
	if defaultBorderTheme(m.config) {
		return ansiDefaultMuted + line + ansiReset
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Border)).Render(line)
}

// cycleInboxGroup switches the inbox between grouping by section and by project.
func (m *Model) cycleInboxGroup() bool {
	if m.displayMode != displayModeInbox {
		return false
	}

	selected := m.selectedSessionIdentity()

	if m.inboxGroup == config.InboxGroupProject {
		m.inboxGroup = config.InboxGroupState
	} else {
		m.inboxGroup = config.InboxGroupProject
	}

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)

	return true
}

// inboxDims reports whether a list row is an inbox row in a dimmed section, or the line of an
// offline host.
func (m *Model) inboxDims(row popupListRow) bool {
	if m.displayMode == displayModeInbox && row.sessionIndex < len(m.filtered) &&
		strings.HasPrefix(m.filtered[row.sessionIndex].ID, inboxOfflineIDPrefix) {
		return true
	}

	item, ok := m.inboxItemAt(row)

	return ok && slices.Contains(m.config.Inbox.Dim, item.section)
}
