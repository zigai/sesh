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

var errNotClosed = errors.New("only a closed conversation is archived")

type workSyncMsg struct {
	changed bool
}

// remoteResume is a conversation resumed on a remote host, whose window the picker attaches to
// once the remote catalog lists its session.
type remoteResume struct {
	connection string
	session    string
	pane       string
}

// hostClosedItems lists the recorded conversations of a host whose agent no longer runs there,
// except those whose pull request ended and those the user archived. A project shows its most
// recent ones up to the configured count, then a row that expands the rest.
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
		if running[recorded.Harness+"/"+recorded.Conversation] || recorded.PREnded() ||
			m.inbox.IsArchived(closedKey(host.identity.ConnectionID, recorded.ID), recorded.TurnAt) {
			continue
		}

		item := m.newClosedItem(host, recorded)
		key := inboxProject(item)

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

// shownClosedItems are the closed rows a project shows: the most recent ones up to the configured
// count unless the user expanded the project, then a row that expands the rest.
func (m *Model) shownClosedItems(host inboxHost, project string, items []inboxItem) []inboxItem {
	shown := m.config.Inbox.ClosedShown
	expandKey := host.identity.ConnectionID + "\x00" + project

	if shown == 0 || len(items) <= shown || m.closedExpanded[expandKey] {
		return items
	}

	slices.SortStableFunc(items, func(left, right inboxItem) int { return right.agent.StatusSince.Compare(left.agent.StatusSince) })

	more := items[shown]
	more.agent.PaneID = moreRowPrefix + project
	more.agent.HarnessSession = strconv.Itoa(len(items)-shown) + " more"
	more.agent.Task = more.agent.HarnessSession
	more.agent.SessionID = ""
	more.agent.SessionPath = ""
	more.agent.GitBranch = ""
	more.work = work.EmptyItem()
	more.more = expandKey

	return append(items[:shown:shown], more)
}

// expandClosedItems shows every closed row of the project a "more" row stands for.
func (m *Model) expandClosedItems(item inboxItem) func() (tea.Model, tea.Cmd) {
	return func() (tea.Model, tea.Cmd) {
		m.closedExpanded[item.more] = true
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

	if agent.HarnessSession == "" {
		agent.HarnessSession = recorded.Harness
	}

	if metadata, ok := m.agentGit[agentGitDirectory(agent)]; ok && host.origin == session.OriginLocal {
		applyAgentGitMetadata(&agent, metadata)
	}

	agent.Host = host.identity.ConnectionID

	var (
		owner session.Session
		live  task.Live
	)

	return inboxItem{
		agent: agent, owner: owner, host: host, live: live, task: false, closed: true, work: recorded, more: "", section: config.InboxClosed,
	}
}

// closedKey names a recorded conversation in the inbox state: work item IDs are only unique on
// one host.
func closedKey(host, id string) string {
	return host + "/" + closedRowPrefix + id
}

// resumeClosedItem resumes a closed conversation on its host and goes to its new window.
func (m *Model) resumeClosedItem(item inboxItem) func() (tea.Model, tea.Cmd) {
	at := action.Local()
	if item.host.origin == session.OriginSSH {
		at = action.Peer(item.host.identity.ConnectionID)
	}

	subject := action.Subject{At: at, Sessions: nil, Pane: "", Task: ""}

	return m.runAction(action.AgentResume, subject, action.Values{"item": {item.work.ID}})
}

// followResume goes to the window a resumed conversation runs in.
func (m *Model) followResume(at action.Location, resumed action.ResumedAgent) tea.Cmd {
	target := resumed.Session.Session(at)

	if at.Kind == action.LocationPeer {
		m.pendingResume = &remoteResume{connection: at.Connection, session: resumed.Session.ID, pane: resumed.Pane}

		return m.attachResumed()
	}

	agent := emptyAgentSession()
	agent.PaneID = resumed.Pane

	return m.runAgentSessionAction(target, agent)
}

// attachResumed attaches to the window of a conversation resumed on a remote host once the remote
// catalog lists its session; a new project session reaches it with the peer's next inventory.
func (m *Model) attachResumed() tea.Cmd {
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

func (m *Model) archiveError(item inboxItem) error {
	switch {
	case !item.closed || item.more != "":
		return errNotClosed
	case m.deps.Inbox == nil:
		return errInboxUnsaved
	default:
		return nil
	}
}

// syncWorkCommand brings the recorded conversations up to date once per picker, after they first
// load, and reloads them when anything changed.
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

// workDirectories are the directories of this machine's recorded conversations that the inbox can
// list, whose git state the closed rows show.
func (m *Model) workDirectories() []string {
	directories := make([]string, 0, len(m.work))

	for _, recorded := range m.work {
		if !recorded.PREnded() {
			directories = append(directories, recorded.Directory)
		}
	}

	return directories
}

// closedPR is the pull request recorded for a closed row, with the issues it closes.
func closedPR(recorded work.Item) string {
	pr := recorded.PR
	if pr.Number == 0 {
		return ""
	}

	text := "#" + strconv.Itoa(pr.Number)
	if pr.State != task.PROpen {
		text += " " + string(pr.State)
	}

	return text
}

func closedIssues(recorded work.Item) string {
	issues := make([]string, 0, len(recorded.Issues))

	for _, issue := range slices.Sorted(slices.Values(recorded.Issues)) {
		issues = append(issues, "#"+strconv.Itoa(issue))
	}

	return strings.Join(issues, " ")
}
