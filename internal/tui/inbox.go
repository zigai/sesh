package tui

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui/providers"
	"github.com/zigai/sesh/internal/work"
)

const (
	inboxAgentRow inboxItemKind = iota
	inboxTaskRow
	inboxClosedRow
	inboxMoreRow
)

const (
	notTaskAction taskActionFit = iota
	taskActionFits
	taskActionMisfits
)

const (
	inboxGroupIDPrefix   = "inbox:"
	inboxOfflineIDPrefix = inboxGroupIDPrefix + "offline:"
	taskRunPollInterval  = time.Second
)

type TaskSource interface {
	LoadTasks(ctx context.Context) ([]task.Live, error)
	SyncTasks(ctx context.Context, titles map[string]string) (bool, error)
}

type taskRunTickMsg struct{}

type taskSyncMsg struct {
	changed bool
}

type inboxItemKind uint8

type taskActionFit uint8

type inboxHost struct {
	origin   session.Origin
	identity session.RuntimeIdentity
	label    string
	tasks    []task.Live
	work     []work.Item
}

type inboxItem struct {
	agent     session.AgentSession
	owner     session.Session
	host      inboxHost
	live      task.Live
	kind      inboxItemKind
	work      work.Item
	expandKey string
	section   string
	hidden    int
}

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

func (m *Model) inboxOfflineRows() []session.Session {
	if !remoteSessionsEnabled(m.config, m.deps) {
		return nil
	}

	var rows []session.Session

	for _, connection := range m.remoteCatalog.Connections {
		label := connection.Label
		if label == "" {
			label = connection.ID
		}

		switch {
		case connection.LastError == RemoteErrorIncompatibleProtocol:
			rows = append(rows, inboxGroupRow("offline:"+connection.ID, label+" needs the same sesh version", nil))
		case remoteConnectionStatus(connection, m.config.Remote.StaleAfter, time.Now()) == session.RemoteStatusUnavailable:
			rows = append(rows, inboxGroupRow("offline:"+connection.ID, label+" offline", nil))
		}
	}

	return rows
}

func (m *Model) inboxStateRows(items []inboxItem) []session.Session {
	rows := make([]session.Session, 0, len(m.config.Inbox.Sections))

	for _, section := range m.config.Inbox.Sections {
		var agents []session.AgentSession

		conversations := 0

		for _, item := range items {
			if item.section == section {
				agents = append(agents, item.agent)
				conversations += max(item.hidden, 1)
			}
		}

		switch {
		case len(agents) == 0:
		case m.sectionCollapsed(section):
			rows = append(rows, inboxGroupRow(section, fmt.Sprintf("%s (%d)", m.config.Inbox.Label(section), conversations), nil))
		default:
			rows = append(rows, inboxGroupRow(section, m.config.Inbox.Label(section), agents))
		}
	}

	return rows
}

func (m *Model) inboxProjectRows(sorted []inboxItem) []session.Session {
	var keys []string

	groups := make(map[string][]session.AgentSession)
	names := make(map[string]string)

	for _, item := range sorted {
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

func (m *Model) sectionCollapsed(section string) bool {
	return slices.Contains(m.config.Inbox.Collapsed, section) && !m.openedSections[section] && m.textInput.Value() == ""
}

func (m *Model) collapsedSectionAt(row popupListRow) (string, bool) {
	if m.displayMode != displayModeInbox || m.inboxGroup != config.InboxGroupState || row.isAgent() || row.isSeparator() ||
		row.sessionIndex < 0 || row.sessionIndex >= len(m.filtered) || len(m.filtered[row.sessionIndex].Agents) > 0 {
		return "", false
	}

	section, ok := strings.CutPrefix(m.filtered[row.sessionIndex].ID, inboxGroupIDPrefix)

	return section, ok && slices.Contains(m.config.Inbox.Collapsed, section)
}

func (m *Model) openSection(section string) func() (tea.Model, tea.Cmd) {
	return func() (tea.Model, tea.Cmd) {
		m.openedSections[section] = true

		m.refreshDisplaySessions()
		m.filterSessions()

		if index := slices.IndexFunc(m.filtered, func(row session.Session) bool { return row.ID == inboxGroupIDPrefix+section }); index >= 0 {
			m.cursor = index
			m.agentCursor = 0
			m.listViewportPolicy = listViewportFollowSelection
		}

		return m, nil
	}
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

func inboxProject(item inboxItem) string {
	switch {
	case item.kind == inboxTaskRow && item.live.Record.Kind == task.KindProject:
		return item.live.Record.Project
	case item.agent.ProjectRoot != "":
		return item.agent.ProjectRoot
	default:
		return item.agent.CWD
	}
}

func (m *Model) compareInboxItems(left, right inboxItem) int {
	sections := m.config.Inbox.Sections
	if order := cmp.Compare(slices.Index(sections, left.section), slices.Index(sections, right.section)); order != 0 {
		return order
	}

	return compareRecency(left, right)
}

func inboxKey(host, paneID string) string {
	return host + "\x00" + paneID
}

func (m *Model) inboxHosts() []inboxHost {
	local := session.RuntimeIdentity{
		ConnectionID: "", ConnectionLabel: "", ConfigDigest: "", PeerID: "", StreamInstance: "", TmuxServerSocket: "",
		TmuxServerPID: 0, TmuxServerStartedAt: time.Time{}, TmuxSessionCreatedAt: time.Time{},
	}

	hosts := []inboxHost{{origin: session.OriginLocal, identity: local, label: "", tasks: m.tasks, work: m.work}}
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
			work:  connection.Work,
		})
	}

	return hosts
}

func (m *Model) hostInboxItems(host inboxHost) []inboxItem {
	agents := m.hostAgentsByPane(host)
	items := append(m.hostAgentItems(host, agents), m.hostTaskItems(host, agents)...)

	return append(items, m.hostClosedItems(host)...)
}

func (m *Model) hostAgentItems(host inboxHost, agents map[string]session.AgentSession) []inboxItem {
	tasks := make(map[string]task.Live, len(host.tasks))

	for _, live := range host.tasks {
		if !taskInFlight(live) && live.AgentPane != "" {
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

			kind := inboxAgentRow

			live, isTask := tasks[agent.PaneID]
			if isTask {
				agent = taskAgent(live, agents)
				kind = inboxTaskRow
			}

			items = append(items, m.newInboxItem(host, row, agent, live, kind))
		}
	}

	return items
}

func (m *Model) hostTaskItems(host inboxHost, agents map[string]session.AgentSession) []inboxItem {
	owners := m.hostSessionsByID(host)

	var items []inboxItem

	for _, live := range host.tasks {
		if _, running := agents[string(live.AgentPane)]; taskInFlight(live) || (running && live.AgentPane != "") {
			continue
		}

		item := m.newInboxItem(host, owners[string(live.SessionID)], taskAgent(live, agents), live, inboxTaskRow)
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

func taskInFlight(live task.Live) bool {
	return (live.Record.State == task.StateCreating || live.Record.State == task.StateFinishing) && !live.Stalled
}

func (m *Model) newInboxItem(host inboxHost, owner session.Session, agent session.AgentSession, live task.Live, kind inboxItemKind) inboxItem {
	if metadata, ok := m.agentGit[agentGitDirectory(agent)]; ok && host.origin == session.OriginLocal {
		applyAgentGitMetadata(&agent, metadata)
	}

	agent.Host = host.identity.ConnectionID

	item := inboxItem{
		agent: agent, owner: owner, host: host, live: live, kind: kind, work: recordedWork(host, agent, live, kind), expandKey: "",
		section: "", hidden: 0,
	}
	item.section = m.inboxSection(item)

	return item
}

func (m *Model) inboxSection(item inboxItem) string {
	record := item.live.Record

	switch {
	case item.kind == inboxTaskRow && (record.State == task.StateSleeping || record.State == task.StateWaking):
		return config.InboxAsleep
	case m.snoozed(item):
		return config.InboxSnoozed
	}

	if section, decided := statusSection(item.agent); decided {
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

func statusSection(agent session.AgentSession) (string, bool) {
	switch agent.Status.DisplayStatus() {
	case session.AgentStatusWaiting:
		switch agent.WaitingReason {
		case string(aht.ActivityDetailPermission):
			return config.InboxApprove, true
		case string(aht.ActivityDetailQuestion):
			return config.InboxInput, true
		}

		return config.InboxWaiting, true
	case session.AgentStatusFailed:
		if agent.FailureReason == string(aht.ActivityDetailUsageLimit) {
			return config.InboxLimited, true
		}

		return config.InboxDone, true
	case session.AgentStatusRunning:
		return config.InboxRunning, true
	case session.AgentStatusMerged:
		return config.InboxIdle, true
	case session.AgentStatusIdle, session.AgentStatusInterrupted, session.AgentStatusUnknown, session.AgentStatusSleeping,
		session.AgentStatusClosed:
	}

	return "", false
}

func (m *Model) unmergedItem(item inboxItem) bool {
	if item.agent.GitUnmerged > 0 || (item.kind == inboxTaskRow && item.live.Record.PR.State == task.PROpen) {
		return true
	}

	if item.agent.Host != "" || item.agent.PaneID == "" {
		return false
	}

	return m.inboxPR(item) != ""
}

func (m *Model) inboxPR(item inboxItem) string {
	if item.agent.Host == "" && item.agent.PaneID != "" {
		if cell := m.providerRunner.Cell(providers.GH, item.agent.PaneID, display.FieldAgentPR); cell.Text != "" {
			return cell.Text
		}
	}

	_, text := rowPR(item)

	return text
}

func rowPR(item inboxItem) (int, string) {
	switch item.kind {
	case inboxClosedRow:
		if linked := item.work.Linked(); len(linked) > 0 {
			return linked[len(linked)-1].Number, linkedPRs(item.work)
		}
	case inboxTaskRow:
		if pr := item.live.Record.PR; pr.Number != 0 {
			return pr.Number, prText(pr.Number, pr.State)
		}
	case inboxAgentRow, inboxMoreRow:
	}

	return 0, ""
}

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

func (host inboxHost) owns(row session.Session) bool {
	return row.Origin == host.origin && !row.Saved() && row.RuntimeIdentity.ConnectionID == host.identity.ConnectionID
}

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

func (m *Model) handleInboxEnter(selected popupListRow) func() (tea.Model, tea.Cmd) {
	if section, ok := m.collapsedSectionAt(selected); ok {
		return m.openSection(section)
	}

	item, ok := m.inboxItemAt(selected)
	if !ok {
		return nil
	}

	if item.kind == inboxTaskRow && !item.live.Present {
		return m.invokeAction(action.TaskOpen.ID, action.Values{})
	}

	if item.kind == inboxMoreRow {
		return m.expandClosedItems(item)
	}

	if item.kind == inboxClosedRow {
		return m.resumeClosedItem(item)
	}

	agent := item.agent
	if item.kind == inboxTaskRow {
		agent.PaneID = string(item.live.AgentPane)
	}

	if item.host.origin == session.OriginSSH {
		return m.attachRemote(item.owner, agent.PaneID)
	}

	return func() (tea.Model, tea.Cmd) {
		return m, m.runAgentSessionAction(item.owner, agent)
	}
}

func (m *Model) recordedPR(agent session.AgentSession) string {
	item, ok := m.inboxItems[inboxKey(agent.Host, agent.PaneID)]
	if !ok || m.displayMode != displayModeInbox {
		return ""
	}

	_, text := rowPR(item)

	return text
}

func (m *Model) footerActions(keys actionKeyMap) []footerPart {
	ids := footerActionIDs[m.displayMode]
	parts := make([]footerPart, 0, len(ids))

	for _, id := range ids {
		index := slices.IndexFunc(keys.actions, func(bound boundAction) bool {
			return bound.binding.Action == id && len(bound.binding.Values) == 0
		})
		if index >= 0 {
			parts = append(parts, newActionFooterPart(keys.actions[index], !m.actionRunnable(id)))
		}
	}

	return parts
}

func taskActionFitOf(id action.ID, live task.Live) taskActionFit {
	state := live.Record.State

	var fits bool

	switch id {
	case action.TaskSleep.ID:
		fits = state == task.StateActive
	case action.TaskWake.ID:
		fits = state == task.StateSleeping || state == task.StateWaking
	case action.TaskOpen.ID:
		fits = !live.Present && !live.Stalled && state != task.StateCreating && state != task.StateFinishing
	case action.TaskFinish.ID, action.TaskRun.ID:
		fits = true
	default:
		return notTaskAction
	}

	if fits {
		return taskActionFits
	}

	return taskActionMisfits
}

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

func (m *Model) inboxDims(row popupListRow) bool {
	if m.displayMode == displayModeInbox && row.sessionIndex < len(m.filtered) &&
		strings.HasPrefix(m.filtered[row.sessionIndex].ID, inboxOfflineIDPrefix) {
		return true
	}

	item, ok := m.inboxItemAt(row)

	return ok && slices.Contains(m.config.Inbox.Dim, item.section)
}
