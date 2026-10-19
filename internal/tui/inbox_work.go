package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

const (
	closedRowPrefix = "work:"
	moreRowPrefix   = "more:"
)

var (
	errNotRecorded     = errors.New("the row has no recorded conversation")
	errAlreadyPinned   = errors.New("the conversation is already pinned")
	errNotPinned       = errors.New("the conversation is not pinned")
	errNothingLinked   = errors.New("no pull request is linked to the conversation")
	errAlreadyFinished = errors.New("the conversation is already finished")
	errTaskPR          = errors.New("a task's pull request is its branch's")
)

type workSyncMsg struct {
	changed bool
}

type remoteResume struct {
	connection string
	session    string
	pane       string
}

func (m *Model) hostClosedItems(host inboxHost) []inboxItem {
	running := make(map[string]bool)

	for _, row := range m.sessions {
		if !host.owns(row) {
			continue
		}

		for _, agent := range row.Agents {
			running[agent.Harness+"/"+agent.SessionID] = true
		}
	}

	var keys []string

	projects := make(map[string][]inboxItem)

	for _, recorded := range host.work {
		if running[recorded.Harness+"/"+recorded.Conversation] || recorded.Task != "" {
			continue
		}

		item := m.newClosedItem(host, recorded)
		key := item.section + ":" + inboxProject(item)

		if _, seen := projects[key]; !seen {
			keys = append(keys, key)
		}

		projects[key] = append(projects[key], item)
	}

	items := make([]inboxItem, 0, len(host.work))

	for _, key := range keys {
		items = append(items, m.shownClosedItems(host, key, projects[key])...)
	}

	return items
}

func (m *Model) shownClosedItems(host inboxHost, group string, items []inboxItem) []inboxItem {
	shown := m.config.Inbox.ClosedShown
	expandKey := host.identity.ConnectionID + "\x00" + group
	unseen := items[0].work.UnseenAt != 0

	if unseen || shown == 0 || len(items) <= shown || m.closedExpanded[expandKey] {
		return items
	}

	slices.SortStableFunc(items, compareRecency)

	return append(items[:shown:shown], newMoreItem(items[shown], group, expandKey, len(items)-shown))
}

func newMoreItem(firstHidden inboxItem, group, expandKey string, hidden int) inboxItem {
	more := firstHidden
	more.agent.PaneID = moreRowPrefix + group
	more.agent.HarnessSession = strconv.Itoa(hidden) + " more"
	more.agent.Task = more.agent.HarnessSession
	more.agent.SessionID = ""
	more.agent.SessionPath = ""
	more.agent.GitBranch = ""
	more.work = work.EmptyItem()
	more.kind = inboxMoreRow
	more.expandKey = expandKey

	return more
}

func (m *Model) expandClosedItems(item inboxItem) func() (tea.Model, tea.Cmd) {
	return func() (tea.Model, tea.Cmd) {
		m.closedExpanded[item.expandKey] = true
		selected := m.selectedSessionIdentity()

		m.refreshDisplaySessions()
		m.filterSessions()
		m.restoreSelectedSession(selected)

		return m, nil
	}
}

func (m *Model) newClosedItem(host inboxHost, recorded work.Item) inboxItem {
	agent := emptyAgentSession()
	agent.PaneID = closedRowPrefix + recorded.ID
	agent.Harness = recorded.Harness
	agent.SessionID = recorded.Conversation
	agent.SessionPath = recorded.SessionPath
	agent.CWD = recorded.Directory
	agent.ProjectRoot = recorded.Project
	agent.GitBranch = recorded.Branch
	agent.HarnessSession = recorded.Title
	agent.Task = recorded.Title
	agent.Status = session.AgentStatusClosed
	agent.StatusSince = time.Unix(recorded.TurnAt, 0)
	agent.Host = host.identity.ConnectionID

	if agent.HarnessSession == "" {
		agent.HarnessSession = recorded.Harness
	}

	if metadata, ok := m.agentGit[agentGitDirectory(agent)]; ok && host.origin == session.OriginLocal {
		applyAgentGitMetadata(&agent, metadata)
	}

	var (
		owner session.Session
		live  task.Live
	)

	section := config.InboxClosed

	switch {
	case recorded.UnseenAt != 0:
		section = config.InboxDone
	case recorded.Finished():
		section = config.InboxFinished
	}

	return inboxItem{
		agent: agent, owner: owner, host: host, live: live, kind: inboxClosedRow, work: recorded, expandKey: "", section: section,
	}
}

func compareRecency(left, right inboxItem) int {
	if left.work.Pinned() != right.work.Pinned() {
		if left.work.Pinned() {
			return -1
		}

		return 1
	}

	return right.agent.StatusSince.Compare(left.agent.StatusSince)
}

func recordedWork(host inboxHost, agent session.AgentSession, live task.Live, kind inboxItemKind) work.Item {
	found := work.EmptyItem()

	for _, recorded := range host.work {
		matches := recorded.Harness == agent.Harness && recorded.Conversation == agent.SessionID && agent.SessionID != ""
		if kind == inboxTaskRow {
			matches = recorded.Task == live.Record.ID
		}

		if matches && (found.ID == "" || recorded.TurnAt > found.TurnAt) {
			found = recorded
		}
	}

	return found
}

func (m *Model) resumeClosedItem(item inboxItem) func() (tea.Model, tea.Cmd) {
	at := action.Local()
	if item.host.origin == session.OriginSSH {
		at = action.Peer(item.host.identity.ConnectionID)
	}

	subject := action.Subject{At: at, Sessions: nil, Pane: "", Task: ""}

	return m.runAction(action.AgentResume, subject, action.Values{"item": {item.work.ID}})
}

func (m *Model) followResume(at action.Location, resumed action.ResumedAgent) tea.Cmd {
	target := resumed.Session.Session(at)

	if at.Kind == action.LocationPeer {
		m.pendingResume = &remoteResume{connection: at.Connection, session: resumed.Session.ID, pane: resumed.Pane}

		return m.attachPendingResumeWhenListed()
	}

	agent := emptyAgentSession()
	agent.PaneID = resumed.Pane

	return m.runAgentSessionAction(target, agent)
}

func (m *Model) attachPendingResumeWhenListed() tea.Cmd {
	pending := m.pendingResume
	if pending == nil {
		return nil
	}

	index := slices.IndexFunc(m.sessions, func(row session.Session) bool {
		return row.Origin == session.OriginSSH && row.RuntimeIdentity.ConnectionID == pending.connection && row.ID == pending.session
	})
	if index < 0 {
		return nil
	}

	m.pendingResume = nil
	m.deferredRemoteAttach = m.sessions[index]
	m.remoteAttachPane = pending.pane
	m.hasDeferredRemoteAttach = true

	return tea.Quit
}

func workActionError(id action.ID, item inboxItem) error {
	switch {
	case id == action.InboxFinish.ID && item.kind == inboxTaskRow:
		return nil
	case item.kind == inboxTaskRow && (id == action.InboxLink.ID || id == action.InboxUnlink.ID):
		return errTaskPR
	case item.work.ID == "":
		return errNotRecorded
	default:
		return recordedActionError(id, item.work)
	}
}

func recordedActionError(id action.ID, recorded work.Item) error {
	switch {
	case id == action.InboxFinish.ID && recorded.Finished():
		return errAlreadyFinished
	case id == action.InboxPin.ID && recorded.Pinned():
		return errAlreadyPinned
	case id == action.InboxUnpin.ID && !recorded.Pinned():
		return errNotPinned
	case id == action.InboxUnlink.ID && len(recorded.Linked()) == 0:
		return errNothingLinked
	default:
		return nil
	}
}

func (m *Model) runWorkAction(id action.ID, item inboxItem, values action.Values) func() (tea.Model, tea.Cmd) {
	if id == action.InboxFinish.ID && item.kind == inboxTaskRow {
		return m.invokeAction(action.TaskFinish.ID, action.Values{})
	}

	at := action.Local()
	if item.host.origin == session.OriginSSH {
		at = action.Peer(item.host.identity.ConnectionID)
	}

	subject := action.Subject{At: at, Sessions: nil, Pane: "", Task: ""}
	params := action.Values{"item": {item.work.ID}}

	switch id {
	case action.InboxPin.ID:
		return m.runAction(action.AgentPin, subject, params)
	case action.InboxUnpin.ID:
		return m.runAction(action.AgentUnpin, subject, params)
	case action.InboxUnseen.ID:
		return m.runAction(action.AgentUnseen, subject, params)
	case action.InboxSeen.ID:
		return m.runAction(action.AgentSeen, subject, params)
	case action.InboxLink.ID:
		params.Set("pr", values.String("pr"))

		return m.runAction(action.AgentLink, subject, params)
	case action.InboxUnlink.ID:
		return m.unlinkPR(item, subject, values.String("pr"))
	default:
		return m.runAction(action.AgentFinish, subject, params)
	}
}

func (m *Model) unlinkPR(item inboxItem, subject action.Subject, pr string) func() (tea.Model, tea.Cmd) {
	linked := item.work.Linked()
	if pr == "" && len(linked) == 1 {
		pr = strconv.Itoa(linked[0].Number)
	}

	if pr != "" {
		return m.runAction(action.AgentUnlink, subject, action.Values{"item": {item.work.ID}, "pr": {pr}})
	}

	entries := make([]menuEntry, 0, len(linked))
	for _, link := range linked {
		number := strconv.Itoa(link.Number)
		entries = append(entries, menuEntry{label: prText(link.Number, link.State), key: "", run: func() (tea.Model, tea.Cmd) {
			return m.runAction(action.AgentUnlink, subject, action.Values{"item": {item.work.ID}, "pr": {number}})()
		}})
	}

	return m.openMenu(&rowMenu{title: "unlink", entries: entries, cursor: 0, anchored: false, anchorX: 0, anchorY: 0, box: menuBox{x: 0, y: 0, width: 0, height: 0}})
}

func (m *Model) syncWorkCommand() tea.Cmd {
	if m.deps.Work == nil || m.workSynced || len(m.work) == 0 {
		return nil
	}

	m.workSynced = true

	ctx := m.operationContext()
	source := m.deps.Work

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		changed, err := source.SyncWork(ctx)

		return workSyncMsg{changed: changed && err == nil}
	}
}

func (m *Model) unfinishedWorkDirectories() []string {
	directories := make([]string, 0, len(m.work))

	for _, recorded := range m.work {
		if !recorded.Finished() {
			directories = append(directories, recorded.Directory)
		}
	}

	return directories
}

func linkedPRs(recorded work.Item) string {
	linked := recorded.Linked()
	texts := make([]string, 0, len(linked))

	for _, pr := range linked {
		texts = append(texts, prText(pr.Number, pr.State))
	}

	return strings.Join(texts, ", ")
}

func prText(number int, state task.PRState) string {
	text := "#" + strconv.Itoa(number)
	if state != task.PROpen && state != "" {
		text += " " + string(state)
	}

	return text
}

func closedIssues(recorded work.Item) string {
	issues := recorded.Issues()
	texts := make([]string, 0, len(issues))

	for _, issue := range issues {
		texts = append(texts, "#"+strconv.Itoa(issue))
	}

	return strings.Join(texts, " ")
}
