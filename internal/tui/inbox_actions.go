package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

const (
	snoozeCustomLabel = "pick a time…"
	openerWaitDelay   = 2 * time.Second
)

var (
	errNotInInbox         = errors.New("works on inbox rows")
	errNoConversation     = errors.New("the agent reports no conversation ID")
	errAlreadySnoozed     = errors.New("the row is already snoozed")
	errNotSnoozed         = errors.New("the row is not snoozed")
	errAlreadySeen        = errors.New("the agent's latest result is already seen")
	errNotSeen            = errors.New("the agent's latest result is not seen yet")
	errNoPullRequest      = errors.New("no pull request is known for the row")
	errRemotePR           = errors.New("pull requests of other machines open there")
	errInboxUnsaved       = errors.New("inbox state is not available")
	errNoEvidence         = errors.New("the agent's latest report lists no evidence")
	errUnknownInboxAct    = errors.New("unknown inbox action")
	errClosedConversation = errors.New("the conversation's agent is closed")
	errNoProject          = errors.New("the row belongs to no project")
	errRemoteProject      = errors.New("tasks of other machines start there")
)

type inboxUpdatedMsg struct {
	state inbox.State
	err   error
}

type openedMsg struct {
	err error
}

func conversationKey(agent session.AgentSession) string {
	if agent.SessionID == "" {
		return ""
	}

	return agent.Host + "/" + agent.Harness + "/" + agent.SessionID
}

func (m *Model) snoozed(item inboxItem) bool {
	snooze, ok := m.inbox.Find(conversationKey(item.agent))
	if !ok || conversationKey(item.agent) == "" {
		return false
	}

	if snooze.Timed() && time.Now().Unix() >= snooze.Until {
		return false
	}

	switch item.agent.Status.DisplayStatus() {
	case session.AgentStatusIdle, session.AgentStatusWaiting, session.AgentStatusFailed:
		return item.agent.StatusSince.UnixNano() <= snooze.SnoozedAt
	case session.AgentStatusRunning, session.AgentStatusInterrupted, session.AgentStatusUnknown,
		session.AgentStatusSleeping, session.AgentStatusMerged, session.AgentStatusClosed:
		return true
	}

	return true
}

func (m *Model) markedUnseen(item inboxItem) bool {
	mark, ok := m.inbox.FindUnseen(conversationKey(item.agent))
	if !ok || conversationKey(item.agent) == "" || item.owner.ID == "" || m.state == nil {
		return false
	}

	return m.state.GetLastAgentView(item.owner.SemanticIdentity(), item.agent.PaneID) < mark.At
}

func (m *Model) selectedTaskLive() task.Live {
	item, ok := m.selectedInboxItem()
	if !ok || item.kind != inboxTaskRow {
		var none task.Live

		return none
	}

	return item.live
}

func (m *Model) pickerActionError(id action.ID) error {
	item, ok := m.selectedInboxItem()
	if !ok {
		return errNotInInbox
	}

	switch id {
	case action.InboxSnooze.ID, action.InboxUnsnooze.ID:
		return m.snoozeError(id, item)
	case action.InboxSeen.ID, action.InboxUnseen.ID:
		return m.seenError(id, item)
	case action.InboxPR.ID:
		return m.pullRequestError(item)
	case action.InboxEvidence.ID:
		if len(m.evidenceOf(item)) == 0 {
			return errNoEvidence
		}

		return nil
	case action.InboxFinish.ID, action.InboxPin.ID, action.InboxUnpin.ID, action.InboxLink.ID, action.InboxUnlink.ID:
		return workActionError(id, item)
	case action.InboxTask.ID:
		return taskProjectError(item)
	default:
		return errUnknownInboxAct
	}
}

func taskProjectError(item inboxItem) error {
	switch {
	case item.agent.Host != "":
		return errRemoteProject
	case rowProject(item) == "":
		return errNoProject
	default:
		return nil
	}
}

func rowProject(item inboxItem) string {
	if item.kind == inboxTaskRow && item.live.Record.Kind == task.KindProject {
		return item.live.Record.Project
	}

	if item.kind == inboxMoreRow {
		return ""
	}

	return item.agent.ProjectRoot
}

func (m *Model) inboxStateError(item inboxItem) error {
	switch {
	case conversationKey(item.agent) == "":
		return errNoConversation
	case m.deps.Inbox == nil:
		return errInboxUnsaved
	default:
		return nil
	}
}

func (m *Model) snoozeError(id action.ID, item inboxItem) error {
	if err := m.inboxStateError(item); err != nil {
		return err
	}

	_, snoozed := m.inbox.Find(conversationKey(item.agent))

	switch {
	case item.kind == inboxClosedRow:
		return errClosedConversation
	case id == action.InboxSnooze.ID && item.section == config.InboxSnoozed:
		return errAlreadySnoozed
	case id == action.InboxUnsnooze.ID && !snoozed:
		return errNotSnoozed
	default:
		return nil
	}
}

func (m *Model) seenError(id action.ID, item inboxItem) error {
	if item.kind == inboxClosedRow {
		return recordedSeenError(id, item)
	}

	if id == action.InboxSeen.ID {
		if item.section != config.InboxReady || item.owner.ID == "" {
			return errAlreadySeen
		}

		return nil
	}

	if err := m.inboxStateError(item); err != nil {
		return err
	}

	if item.section == config.InboxReady || item.owner.ID == "" {
		return errNotSeen
	}

	return nil
}

func recordedSeenError(id action.ID, item inboxItem) error {
	switch {
	case item.work.ID == "":
		return errNotRecorded
	case id == action.InboxSeen.ID && item.work.UnseenAt == 0:
		return errAlreadySeen
	case id == action.InboxUnseen.ID && item.work.UnseenAt != 0:
		return errNotSeen
	default:
		return nil
	}
}

func (m *Model) pullRequestError(item inboxItem) error {
	switch {
	case m.inboxPR(item) == "":
		return errNoPullRequest
	case item.agent.Host != "":
		return errRemotePR
	default:
		return nil
	}
}

func (m *Model) runPickerAction(request pendingAction) func() (tea.Model, tea.Cmd) {
	id := request.entry.Describe().ID

	item, ok := m.selectedInboxItem()
	if err := m.pickerActionError(id); !ok || err != nil {
		m.setErrorStatusf("%s unavailable: %v", request.entry.Describe().Title, err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	key := conversationKey(item.agent)

	switch id {
	case action.InboxSnooze.ID:
		return m.snoozeItem(key, request.values.String("until"))
	case action.InboxUnsnooze.ID:
		return m.updateInbox(func(state *inbox.State) error { state.Remove(key); return nil })
	case action.InboxSeen.ID, action.InboxUnseen.ID:
		if item.kind == inboxClosedRow {
			return m.runWorkAction(id, item, request.values)
		}

		return m.markSeen(id, item, key)
	case action.InboxPR.ID:
		return m.openPullRequest(item)
	case action.InboxEvidence.ID:
		return m.openEvidenceMenu(item)
	case action.InboxFinish.ID, action.InboxPin.ID, action.InboxUnpin.ID, action.InboxLink.ID, action.InboxUnlink.ID:
		return m.runWorkAction(id, item, request.values)
	case action.InboxTask.ID:
		m.taskProject = rowProject(item)

		return func() (tea.Model, tea.Cmd) { return m, tea.Quit }
	}

	return func() (tea.Model, tea.Cmd) { return m, nil }
}

func (m *Model) markSeen(id action.ID, item inboxItem, key string) func() (tea.Model, tea.Cmd) {
	if id == action.InboxSeen.ID {
		if item.owner.ID != "" {
			m.recordActionState(func(current *session.State) {
				current.RecordAgentView(item.owner.SemanticIdentity(), item.agent.PaneID)
			})
		}

		return m.updateInbox(func(state *inbox.State) error { state.ClearUnseen(key); return nil })
	}

	return m.updateInbox(func(state *inbox.State) error { state.MarkUnseen(key, time.Now()); return nil })
}

func (m *Model) snoozeItem(key, until string) func() (tea.Model, tea.Cmd) {
	if until == "" {
		entries := make([]menuEntry, 0, len(m.config.Inbox.Snooze)+1)
		for _, choice := range m.config.Inbox.Snooze {
			entries = append(entries, menuEntry{label: choice, key: "", run: func() (tea.Model, tea.Cmd) { return m.snoozeItem(key, choice)() }})
		}

		param, _ := action.InboxSnooze.Describe().Param("until")
		subject, _ := m.actionSubject(action.InboxSnooze.Describe())
		request := pendingAction{entry: action.InboxSnooze, subject: subject, values: action.Values{}, param: emptyParam()}

		entries = append(entries, menuEntry{label: snoozeCustomLabel, key: "", run: func() (tea.Model, tea.Cmd) { return m.promptParam(request, param)() }})

		return m.openMenu(&rowMenu{entries: entries, cursor: 0, anchored: false, anchorX: 0, anchorY: 0, box: menuBox{x: 0, y: 0, width: 0, height: 0}})
	}

	snooze, err := config.ParseSnooze(until)
	if err != nil {
		m.setErrorStatusf("Snooze failed: %v", err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	now := time.Now()
	ends := snooze.Until(now)

	return m.updateInbox(func(state *inbox.State) error {
		var untilUnix int64
		if !ends.IsZero() {
			untilUnix = ends.Unix()
		}

		state.Expire(now)
		state.Put(inbox.Snooze{Key: key, Until: untilUnix, SnoozedAt: now.UnixNano()})

		return nil
	})
}

func (m *Model) updateInbox(change func(*inbox.State) error) func() (tea.Model, tea.Cmd) {
	store := m.deps.Inbox
	ctx := m.operationContext()

	return func() (tea.Model, tea.Cmd) {
		return m, func() tea.Msg {
			if !m.lifecycle.BeginWorker() {
				return nil
			}
			defer m.lifecycle.EndWorker()

			state, err := store.UpdateInbox(ctx, change)

			return inboxUpdatedMsg{state: state, err: err}
		}
	}
}

func (m *Model) handleInboxUpdated(msg inboxUpdatedMsg) {
	if msg.err != nil {
		logTUIFailure(m.operationContext(), tuiOperationSaveInbox, msg.err)
		m.setErrorStatusf("Failed to save inbox: %v", msg.err)

		return
	}

	m.inbox = msg.state
	selected := m.selectedSessionIdentity()

	m.refreshDisplaySessions()
	m.filterSessions()
	m.restoreSelectedSession(selected)
}

func (m *Model) openPullRequest(item inboxItem) func() (tea.Model, tea.Cmd) {
	target := item.agent.GitBranch
	if number, _ := rowPR(item); number != 0 {
		target = strconv.Itoa(number)
	}

	return m.runOpener(item.agent.CWD, "gh", "pr", "view", target, "--web")
}

func (m *Model) runOpener(directory string, program string, args ...string) func() (tea.Model, tea.Cmd) {
	ctx := m.operationContext()

	return func() (tea.Model, tea.Cmd) {
		return m, func() tea.Msg {
			if !m.lifecycle.BeginWorker() {
				return nil
			}
			defer m.lifecycle.EndWorker()

			return openedMsg{err: startOpener(ctx, directory, program, args...)}
		}
	}
}

func startOpener(ctx context.Context, directory string, program string, args ...string) error {
	command := exec.CommandContext(context.WithoutCancel(ctx), program, args...)
	command.Dir = directory

	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	command.WaitDelay = openerWaitDelay

	if err := command.Start(); err != nil {
		return fmt.Errorf("start opener: %w", err)
	}

	go func() { _ = command.Wait() }()

	return nil
}

func (m *Model) handleOpened(msg openedMsg) {
	if msg.err != nil {
		logTUIFailure(m.operationContext(), tuiOperationOpen, msg.err)
		m.setErrorStatusf("Open failed: %v", msg.err)
	}
}

func (m *Model) inboxDoneByMark(item inboxItem) bool {
	status := item.agent.Status.DisplayStatus()

	return (status == session.AgentStatusIdle || status == session.AgentStatusInterrupted) && m.markedUnseen(item)
}
