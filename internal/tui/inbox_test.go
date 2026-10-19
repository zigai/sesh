package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

const (
	inboxTestHost    = "devbox"
	inboxTestHarness = "claude"
)

var errTranscriptUnreadable = errors.New("transcript unreadable")

type memoryInbox struct {
	state inbox.State
}

func (store *memoryInbox) LoadInbox(context.Context) (inbox.State, error) {
	return store.state, nil
}

func (store *memoryInbox) UpdateInbox(_ context.Context, change func(*inbox.State) error) (inbox.State, error) {
	err := change(&store.state)

	return store.state, err
}

func inboxTestRecord(id string, state task.State) task.Record {
	record := task.EmptyRecord()
	record.ID = id
	record.Kind = task.KindProject
	record.State = state
	record.Project = "/work/api"
	record.Harness = inboxTestHarness
	record.Directory = "/work/trees/api-" + id
	record.Branch = "wt/main/" + id
	record.Base = "main"
	record.Title = id

	return record
}

func inboxTestLive(record task.Record, pane string) task.Live {
	return task.Live{
		Record: record, Present: pane != "", SessionID: "$1", SessionName: testSupportSessionAlphaName, WindowID: "@1", WindowIndex: 2,
		WindowName: record.Title, AgentPane: gotmux.PaneID(pane),
	}
}

func inboxTestAgent(name, pane string, status session.AgentStatus, since time.Time) session.AgentSession {
	agent := emptyAgentSession()
	agent.PaneID = pane
	agent.Harness = inboxTestHarness
	agent.HarnessSession = name
	agent.SessionID = "conversation-" + name
	agent.ProjectRoot = "/work/api"
	agent.CWD = "/work/api"
	agent.Status = status
	agent.StatusSince = since

	return agent
}

func newInboxTestModel(t *testing.T, tasks []task.Live, agents []session.AgentSession) *Model {
	t.Helper()

	model := newTestModel()
	model.sessions = []session.Session{{
		ID: "$1", Name: testSupportSessionAlphaName, Kind: session.KindLive, Origin: session.OriginLocal, Agents: agents, AgentTotal: len(agents),
	}}
	model.tasks = tasks
	model.deps.Inbox = &memoryInbox{state: inbox.State{Version: inbox.Version, Snoozes: nil, Unseen: nil}}
	model.displayMode = displayModeInbox
	refreshInbox(model)

	return model
}

func refreshInbox(model *Model) {
	model.refreshDisplaySessions()
	model.filterSessions()
}

func markViewed(model *Model, pane string, at time.Time) {
	model.state.RecordAgentViewAt(model.sessions[0].SemanticIdentity(), pane, at)
}

func inboxRowsForTest(model *Model) map[string][]string {
	rows := make(map[string][]string)

	for _, group := range model.filtered {
		for _, agent := range group.Agents {
			rows[group.Name] = append(rows[group.Name], agent.HarnessSession)
		}
	}

	return rows
}

func selectInboxRow(t *testing.T, model *Model, name string) {
	t.Helper()

	for groupIndex, group := range model.filtered {
		for agentIndex, agent := range group.Agents {
			if agent.HarnessSession == name {
				model.selectListRow(popupListRow{sessionIndex: groupIndex, agentIndex: agentIndex})

				return
			}
		}
	}

	t.Fatalf("no inbox row %q in %v", name, inboxRowsForTest(model))
}

func runHandler(t *testing.T, model *Model, handler func() (tea.Model, tea.Cmd)) {
	t.Helper()

	if handler == nil {
		t.Fatal("handler is nil")
	}

	_, cmd := handler()
	if cmd != nil {
		updateTestModel(t, model, cmd())
	}
}

func assertInboxRows(t *testing.T, model *Model, want map[string][]string) {
	t.Helper()

	got := inboxRowsForTest(model)
	for section, names := range want {
		if strings.Join(got[section], ",") != strings.Join(names, ",") {
			t.Errorf("section %q = %v, want %v", section, got[section], names)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("sections = %v, want only %v", got, want)
	}
}

func TestInboxSectionsFollowAgentStateSeenMarksAndTasks(t *testing.T) {
	now := time.Now()
	older, newer := now.Add(-2*time.Hour), now.Add(-time.Hour)

	commits := inboxTestAgent("commits", "%5", session.AgentStatusIdle, older)
	commits.GitUnmerged = 2

	merged := inboxTestRecord("merged", task.StateActive)
	merged.PR = task.PullRequest{Number: 4, State: task.PRMerged, URL: ""}

	approval := inboxTestAgent("approval", "%8", session.AgentStatusWaiting, older)
	approval.WaitingReason = "permission"
	question := inboxTestAgent("question", "%9", session.AgentStatusWaiting, older)
	question.WaitingReason = "question"
	limited := inboxTestAgent("limited", "%10", session.AgentStatusFailed, older)
	limited.FailureReason = "usage_limit"

	stalled := inboxTestLive(inboxTestRecord("stalled", task.StateCreating), "")
	stalled.Stalled = true

	model := newInboxTestModel(t, []task.Live{
		inboxTestLive(inboxTestRecord("asleep", task.StateSleeping), ""),
		inboxTestLive(merged, ""),
		stalled,
		inboxTestLive(inboxTestRecord("starting", task.StateCreating), ""),
	}, []session.AgentSession{
		inboxTestAgent("busy", "%1", session.AgentStatusRunning, older),
		inboxTestAgent("asking", "%2", session.AgentStatusWaiting, older),
		inboxTestAgent("fresh-old", "%3", session.AgentStatusIdle, older),
		inboxTestAgent("fresh-new", "%4", session.AgentStatusIdle, newer),
		commits,
		inboxTestAgent("seen", "%6", session.AgentStatusIdle, older),
		inboxTestAgent("broke", "%7", session.AgentStatusFailed, older),
		approval, question, limited,
	})
	markViewed(model, "%5", now)
	markViewed(model, "%6", now)
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		config.InboxDone:     {"fresh-new", "fresh-old", "broke", "stalled"},
		config.InboxApprove:  {"approval"},
		config.InboxInput:    {"question"},
		config.InboxWaiting:  {"asking"},
		config.InboxLimited:  {"limited"},
		config.InboxUnmerged: {"commits"},
		config.InboxRunning:  {"busy"},
		config.InboxAsleep:   {"asleep"},
		config.InboxIdle:     {"seen", "merged"},
	})
}

func TestInboxSectionsFollowTheConfiguredOrderLabelsAndHiddenSections(t *testing.T) {
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("busy", "%1", session.AgentStatusRunning, time.Now()),
		inboxTestAgent("asking", "%2", session.AgentStatusWaiting, time.Now()),
		inboxTestAgent("fresh", "%3", session.AgentStatusIdle, time.Now()),
	})
	model.config.Inbox.Sections = []string{config.InboxWaiting, config.InboxDone}
	model.config.Inbox.Labels = map[string]string{config.InboxDone: "new"}
	refreshInbox(model)

	if names := sessionNamesForTest(model.filtered); !slices.Equal(names, []string{config.InboxWaiting, "new"}) {
		t.Fatalf("sections = %v, want waiting then new", names)
	}
}

func TestSnoozeUntilNextMessageEndsWhenTheAgentFinishesAnotherTurn(t *testing.T) {
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now().Add(-time.Minute)),
	})

	runHandler(t, model, model.invokeAction(action.InboxSnooze.ID, action.Values{"until": {config.SnoozeNextMessage}}))
	assertInboxRows(t, model, map[string][]string{config.InboxSnoozed: {"fresh"}})

	model.sessions[0].Agents[0].Status = session.AgentStatusRunning
	model.sessions[0].Agents[0].StatusSince = time.Now().Add(time.Second)
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxSnoozed: {"fresh"}})

	model.sessions[0].Agents[0].Status = session.AgentStatusIdle
	model.sessions[0].Agents[0].StatusSince = time.Now().Add(2 * time.Second)
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxDone: {"fresh"}})
}

func TestTimedSnoozeEndsAtItsTime(t *testing.T) {
	agent := inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now().Add(-time.Hour))
	model := newInboxTestModel(t, nil, []session.AgentSession{agent})
	snooze := inbox.Snooze{Key: conversationKey(agent), Until: time.Now().Add(time.Hour).Unix(), SnoozedAt: time.Now().UnixNano()}

	model.inbox.Put(snooze)
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxSnoozed: {"fresh"}})

	snooze.Until = time.Now().Add(-time.Minute).Unix()
	model.inbox.Put(snooze)
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxDone: {"fresh"}})
}

func TestSnoozeWithoutATimeOffersTheConfiguredChoices(t *testing.T) {
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now()),
	})
	model.config.Inbox.Snooze = []string{"30m", config.SnoozeNextMessage}

	runHandler(t, model, model.invokeAction(action.InboxSnooze.ID, action.Values{}))

	if model.menu == nil || model.dialog != dialogNone {
		t.Fatalf("snooze without a time opened menu %v and dialog %v, want only the choices", model.menu, model.dialog)
	}

	labels := make([]string, 0, len(model.menu.entries))
	for _, entry := range model.menu.entries {
		labels = append(labels, entry.label)
	}

	if !slices.Equal(labels, []string{"30m", config.SnoozeNextMessage, snoozeCustomLabel}) {
		t.Fatalf("choices = %v", labels)
	}

	runHandler(t, model, model.runMenuEntry(0))
	assertInboxRows(t, model, map[string][]string{config.InboxSnoozed: {"fresh"}})
}

func TestMarkUnseenReturnsARowToDoneUntilItIsSeenAgain(t *testing.T) {
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("seen", "%1", session.AgentStatusIdle, time.Now().Add(-time.Hour)),
	})
	markViewed(model, "%1", time.Now().Add(-time.Minute))
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxIdle: {"seen"}})

	runHandler(t, model, model.invokeAction(action.InboxUnseen.ID, action.Values{}))
	assertInboxRows(t, model, map[string][]string{config.InboxDone: {"seen"}})

	runHandler(t, model, model.invokeAction(action.InboxSeen.ID, action.Values{}))
	assertInboxRows(t, model, map[string][]string{config.InboxIdle: {"seen"}})
}

func TestInboxGroupsByProjectAndMachineKeepingEqualPaneIDsApart(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	web := inboxTestAgent("web-agent", "%2", session.AgentStatusWaiting, time.Now())
	web.ProjectRoot = "/work/web"

	model := loaderTestModel(t, cfg, Dependencies{})
	model.sessions = []session.Session{{
		ID: "$1", Name: testSupportSessionAlphaName, Kind: session.KindLive, Origin: session.OriginLocal, AgentTotal: 2,
		Agents: []session.AgentSession{inboxTestAgent("here", "%1", session.AgentStatusRunning, time.Now()), web},
	}}
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: time.Now(),
			Sessions: []session.Session{{
				ID: "$1", Name: "shop", Kind: session.KindLive, AgentTotal: 1,
				Agents: []session.AgentSession{inboxTestAgent("there", "%1", session.AgentStatusRunning, time.Now())},
			}},
			Tasks: nil,
		}},
	}})
	model.displayMode = displayModeInbox
	model.inboxGroup = config.InboxGroupProject
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		"web":        {"web-agent"},
		"api":        {"here"},
		"api@devbox": {"there"},
	})

	selectInboxRow(t, model, "there")

	if item, ok := model.selectedInboxItem(); !ok || item.agent.HarnessSession != "there" || item.host.label != inboxTestHost {
		t.Fatalf("selected = %q on %q, want there on devbox", item.agent.HarnessSession, item.host.label)
	}
}

func TestInboxActsOnRemoteTasksOnTheirMachine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	model := loaderTestModel(t, cfg, Dependencies{})
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: time.Now(),
			Sessions: []session.Session{{
				ID: "$1", Name: "shop", Kind: session.KindLive, AgentTotal: 1,
				Agents: []session.AgentSession{inboxTestAgent("t7", "%1", session.AgentStatusRunning, time.Now())},
			}},
			Tasks: []task.Live{inboxTestLive(inboxTestRecord("t7", task.StateActive), "%1")},
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)
	selectInboxRow(t, model, "t7")

	subject, ok := model.actionSubject(action.TaskSleep.Describe())
	if !ok || subject.At != action.Peer(inboxTestHost) || subject.Task != "t7" {
		t.Fatalf("subject = %+v, %v; want t7 on devbox", subject, ok)
	}
}

func TestEnterOnTaskFocusesItsAgentPaneInTheOwningSession(t *testing.T) {
	model := newInboxTestModel(t, []task.Live{inboxTestLive(inboxTestRecord("t1", task.StateActive), "%9")}, nil)

	activated := ""

	deps := dependenciesForTest(model)
	deps.TmuxActivatePane = func(_ context.Context, sessionName string, paneID string) error {
		activated = sessionName + " " + paneID
		return nil
	}

	_, cmd := model.handleInboxEnter(model.selectedListRow())()
	if msg, ok := cmd().(sessionActionResultMsg); !ok || msg.err != nil || activated != "alpha %9" {
		t.Fatalf("enter = %#v, activated %q; want alpha %%9", msg, activated)
	}
}

func TestInboxHeadingsAreNotSelectable(t *testing.T) {
	model := newInboxTestModel(t, []task.Live{inboxTestLive(inboxTestRecord("t1", task.StateActive), "%1")}, nil)

	if model.rowSelectable(popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex}) {
		t.Fatal("section heading is selectable")
	}

	if found, ok := model.selectedInboxItem(); !ok || found.live.Record.ID != "t1" {
		t.Fatalf("selected task = %q/%v, want t1", found.live.Record.ID, ok)
	}
}

func TestRowMenuListsOnlyTheEntriesThatApply(t *testing.T) {
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now()),
	})
	model.config.Menu.Agent = []string{config.MenuGo, config.MenuDivider, config.MenuUnsnooze, config.MenuSnooze, config.MenuSeen, config.MenuMerge, config.MenuSleep}

	runHandler(t, model, model.openRowMenu())

	if model.menu == nil {
		t.Fatal("row menu did not open")
	}

	labels := make([]string, 0, len(model.menu.entries))
	for _, entry := range model.menu.entries {
		labels = append(labels, entry.label)
	}

	if !slices.Equal(labels, []string{"go", "", config.MenuSnooze, config.MenuSeen}) {
		t.Fatalf("menu = %q, want go, a divider, snooze and seen", labels)
	}

	updateTestModel(t, model, keyCode(tea.KeyEscape))

	if model.menu != nil {
		t.Fatal("escape left the menu open")
	}
}

func TestRowMenuOffersMergeForCommitsNotOnTheBase(t *testing.T) {
	agent := inboxTestAgent("ahead", "%1", session.AgentStatusIdle, time.Now())
	agent.GitUnmerged = 1
	model := newInboxTestModel(t, nil, []session.AgentSession{agent})
	model.config.Menu.Agent = []string{config.MenuMerge}

	runHandler(t, model, model.openRowMenu())

	if model.menu == nil || len(model.menu.entries) != 1 || model.menu.entries[0].label != config.MenuMerge {
		t.Fatalf("menu = %+v, want merge", model.menu)
	}
}

func TestMergedTaskShowsRecordedPullRequestOnceGitHubNoLongerListsIt(t *testing.T) {
	record := inboxTestRecord("t1", task.StateActive)
	record.PR = task.PullRequest{Number: 42, State: task.PRMerged, URL: ""}
	model := newInboxTestModel(t, []task.Live{inboxTestLive(record, "%1")}, nil)
	model.width, model.height = 160, 30

	if frame := stripANSI(model.renderFrame()); !strings.Contains(frame, "#42") {
		t.Fatalf("frame lacks recorded PR:\n%s", frame)
	}
}

func TestDetailNamesWhyAMergeStopped(t *testing.T) {
	record := inboxTestRecord("t1", task.StateActive)
	record.Runs = []task.Run{{Action: task.MergeAction, Window: task.MergeAction, Status: task.RunFailed, ExitCode: task.MergeExitConflict, StartedAt: 1, FinishedAt: 2}}
	model := newInboxTestModel(t, []task.Live{inboxTestLive(record, "%1")}, nil)
	model.width, model.height = 160, 30

	if frame := stripANSI(model.renderFrame()); !strings.Contains(frame, "merge conflict") {
		t.Fatalf("frame lacks the merge conflict:\n%s", frame)
	}
}

func TestCyclingThroughEmptyInboxKeepsTheSessionSelection(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{ID: "$1", Name: "first", Kind: session.KindLive},
		{ID: "$2", Name: "second", Kind: session.KindLive},
	}
	model.displayModes = []displayMode{displayModeSesh, displayModeInbox}
	refreshInbox(model)
	model.cursor = 1

	model.cycleDisplayMode()
	model.cycleDisplayMode()

	if got := model.filtered[model.cursor].ID; got != "$2" {
		t.Fatalf("selection after INBOX = %s, want $2", got)
	}
}

func TestUnreachableHostShowsOneDimmedOfflineLine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	model := loaderTestModel(t, cfg, Dependencies{})
	model.sessions = []session.Session{{
		ID: "$1", Name: testSupportSessionAlphaName, Kind: session.KindLive, Origin: session.OriginLocal, AgentTotal: 1,
		Agents: []session.AgentSession{inboxTestAgent("here", "%1", session.AgentStatusRunning, time.Now())},
	}}
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: false, LastSeen: time.Now().Add(-time.Hour), LastError: "connection_failed",
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)
	model.width, model.height = 120, 20

	frame := stripANSI(model.renderFrame())
	if strings.Count(frame, "devbox offline") != 1 || strings.Contains(frame, "Sessions") {
		t.Fatalf("frame does not show devbox offline once, without a sessions divider:\n%s", frame)
	}

	last := popupListRow{sessionIndex: len(model.filtered) - 1, agentIndex: parentAgentIndex}
	if !model.inboxDims(last) || model.rowSelectable(last) {
		t.Fatal("offline line is not a dimmed, unselectable heading")
	}
}

func TestAHostOnAnotherProtocolAsksForTheSameSeshVersion(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	model := loaderTestModel(t, cfg, Dependencies{})
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: false, LastSeen: time.Now(), LastError: RemoteErrorIncompatibleProtocol,
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)
	model.width, model.height = 120, 20

	frame := stripANSI(model.renderFrame())
	if !strings.Contains(frame, "devbox needs the same sesh version") || strings.Contains(frame, "devbox offline") {
		t.Fatalf("frame does not ask for the same sesh version on devbox:\n%s", frame)
	}
}

func TestInboxActsOnARemoteTaskWithoutAWindowOnItsMachine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	model := loaderTestModel(t, cfg, Dependencies{})
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: time.Now(),
			Tasks: []task.Live{inboxTestLive(inboxTestRecord("t8", task.StateSleeping), "")},
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)
	selectInboxRow(t, model, "t8")

	subject, ok := model.actionSubject(action.TaskWake.Describe())
	if !ok || subject.At != action.Peer(inboxTestHost) || subject.Task != "t8" {
		t.Fatalf("subject = %+v, %v; want t8 on devbox", subject, ok)
	}
}

type fakeReports struct {
	reports map[string]string
	reads   []string
	err     error
}

func (source *fakeReports) LastReport(_ context.Context, agent session.AgentSession) (string, error) {
	source.reads = append(source.reads, agent.SessionID)

	return source.reports[agent.SessionID], source.err
}

func loadEvidence(t *testing.T, model *Model) {
	t.Helper()

	if cmd := model.loadEvidenceCommand(); cmd != nil {
		updateTestModel(t, model, cmd())
	}
}

func TestInboxReadsEachFinishedTurnsEvidenceOnceAndShowsIt(t *testing.T) {
	finished := inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now().Add(-time.Minute))
	finished.SessionPath = "/history/fresh.jsonl"
	busy := inboxTestAgent("busy", "%2", session.AgentStatusRunning, time.Now())
	busy.SessionPath = "/history/busy.jsonl"

	model := newInboxTestModel(t, nil, []session.AgentSession{finished, busy})
	model.width, model.height = 160, 30
	reports := &fakeReports{reports: map[string]string{
		finished.SessionID: "Done.\n\n**Evidence**\n- `shots/login.png`\n- https://example.com/pr/4\n",
	}}
	model.deps.Evidence = reports

	loadEvidence(t, model)
	loadEvidence(t, model)

	if !slices.Equal(reports.reads, []string{finished.SessionID}) {
		t.Fatalf("reports read = %v, want only the finished agent's, once", reports.reads)
	}

	selectInboxRow(t, model, "fresh")

	frame := stripANSI(model.renderFrame())
	if !strings.Contains(frame, "2 ev") || !strings.Contains(frame, "shots/login.png · https://example.com/pr/4") {
		t.Fatalf("frame lacks the evidence count or items:\n%s", frame)
	}

	runHandler(t, model, model.invokeAction(action.InboxEvidence.ID, action.Values{}))

	if model.menu == nil || len(model.menu.entries) != 2 || model.menu.entries[0].label != "shots/login.png" {
		t.Fatalf("evidence menu = %+v, want the two items", model.menu)
	}

	model.sessions[0].Agents[0].StatusSince = time.Now()
	refreshInbox(model)
	loadEvidence(t, model)

	if len(reports.reads) != 2 {
		t.Fatalf("reports read = %v, want the report read again after a new turn", reports.reads)
	}
}

func TestInboxTriesAnUnreadableReportAgainOnlyAfterTheNextTurn(t *testing.T) {
	finished := inboxTestAgent("fresh", "%1", session.AgentStatusIdle, time.Now().Add(-time.Minute))
	finished.SessionPath = "/history/fresh.jsonl"

	model := newInboxTestModel(t, nil, []session.AgentSession{finished})
	reports := &fakeReports{reports: nil, err: errTranscriptUnreadable}
	model.deps.Evidence = reports

	loadEvidence(t, model)
	loadEvidence(t, model)

	if len(reports.reads) != 1 {
		t.Fatalf("reports read = %v, want one attempt for the turn", reports.reads)
	}

	model.sessions[0].Agents[0].StatusSince = time.Now()
	refreshInbox(model)
	loadEvidence(t, model)

	if len(reports.reads) != 2 {
		t.Fatalf("reports read = %v, want another attempt after a new turn", reports.reads)
	}
}

func TestInboxDoesNotReadTheReportsOfRemoteAgents(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	remoteAgent := inboxTestAgent("there", "%1", session.AgentStatusIdle, time.Now())
	remoteAgent.SessionPath = "/history/there.jsonl"

	model := loaderTestModel(t, cfg, Dependencies{})
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: time.Now(),
			Sessions: []session.Session{{ID: "$1", Name: "shop", Kind: session.KindLive, AgentTotal: 1, Agents: []session.AgentSession{remoteAgent}}},
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)

	reports := &fakeReports{reports: nil}
	model.deps.Evidence = reports
	loadEvidence(t, model)

	if len(reports.reads) != 0 {
		t.Fatalf("reports read = %v, want none for another machine's agent", reports.reads)
	}
}

func closedTestItem(id, conversation string, turnAt time.Time) work.Item {
	item := work.EmptyItem()
	item.ID = id
	item.Harness = inboxTestHarness
	item.Conversation = conversation
	item.Resume = []string{"claude", "--resume", conversation}
	item.Title = "closed " + id
	item.Directory = "/work/api"
	item.Project = "/work/api"
	item.TurnAt = turnAt.Unix()

	return item
}

func TestInboxListsARecordedConversationOnlyAfterItsAgentCloses(t *testing.T) {
	running := inboxTestAgent("running", "%1", session.AgentStatusIdle, time.Now())
	model := newInboxTestModel(t, nil, []session.AgentSession{running})
	model.work = []work.Item{
		closedTestItem("w1", running.SessionID, time.Now()),
		closedTestItem("w2", "conversation-gone", time.Now()),
	}
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		config.InboxDone:   {"running"},
		config.InboxClosed: {"closed w2"},
	})
}

func TestInboxLeavesOutFinishedAndTaskConversations(t *testing.T) {
	model := newInboxTestModel(t, nil, nil)
	finished := closedTestItem("w1", "conversation-finished", time.Now())
	finished.FinishedAt, finished.FinishedBy = time.Now().Unix(), work.FinishedByPR
	ofTask := closedTestItem("w3", "conversation-task", time.Now())
	ofTask.Task = "t1"
	model.work = []work.Item{finished, closedTestItem("w2", "conversation-open", time.Now()), ofTask}
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{config.InboxClosed: {"closed w2"}})
}

func recordingDispatcher(model *Model, dispatched *action.Invocation) {
	inboxState := model.deps.Inbox
	model.deps = testServices(Dependencies{RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
		*dispatched = invocation

		return action.ChangedWork{Item: "w1"}, nil
	}})
	model.deps.Inbox = inboxState
}

func TestFinishOnAClosedConversationFinishesItOnItsMachine(t *testing.T) {
	var dispatched action.Invocation

	model := newInboxTestModel(t, nil, nil)
	recordingDispatcher(model, &dispatched)
	model.work = []work.Item{closedTestItem("w1", "conversation-one", time.Now())}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.invokeAction(action.InboxFinish.ID, action.Values{}))

	input, ok := dispatched.Input.(action.ChangeWork)
	if dispatched.Action != action.AgentFinish.ID || !ok || input.Item != "w1" || dispatched.At != action.Local() {
		t.Fatalf("dispatched %+v, want agent.finish of w1 on this machine", dispatched)
	}
}

func TestFinishOnATaskRowFinishesTheTask(t *testing.T) {
	model := newInboxTestModel(t, []task.Live{inboxTestLive(inboxTestRecord("feature", task.StateActive), "%9")}, nil)
	selectInboxRow(t, model, "feature")

	runHandler(t, model, model.invokeAction(action.InboxFinish.ID, action.Values{}))

	if model.pending == nil || model.pending.entry.Describe().ID != action.TaskFinish.ID {
		t.Fatalf("pending = %+v, want task.finish waiting for confirmation", model.pending)
	}
}

func TestPinnedRowsComeFirstAndStayAmongTheShownClosedRows(t *testing.T) {
	now := time.Now()
	model := newInboxTestModel(t, nil, nil)
	model.config.Inbox.ClosedShown = 2

	pinned := closedTestItem("w1", "conversation-w1", now.Add(-time.Hour))
	pinned.PinnedAt = now.Unix()
	model.work = []work.Item{pinned, closedTestItem("w2", "conversation-w2", now), closedTestItem("w3", "conversation-w3", now.Add(-time.Minute))}
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{config.InboxClosed: {"closed w1", "closed w2", "1 more"}})
}

func TestUnlinkOffersTheLinkedPullRequestsWhenThereAreSeveral(t *testing.T) {
	var dispatched action.Invocation

	model := newInboxTestModel(t, nil, nil)
	recordingDispatcher(model, &dispatched)

	linked := closedTestItem("w1", "conversation-one", time.Now())
	linked.PRs = []work.PRLink{
		{Number: 4, State: task.PRMerged, URL: "", Issues: []int{}, Source: work.LinkBranch},
		{Number: 9, State: task.PROpen, URL: "", Issues: []int{}, Source: work.LinkUser},
	}
	model.work = []work.Item{linked}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.invokeAction(action.InboxUnlink.ID, action.Values{}))

	if model.menu == nil || len(model.menu.entries) != 2 || model.menu.entries[1].label != "#9" {
		t.Fatalf("menu = %+v, want #4 merged and #9", model.menu)
	}

	runHandler(t, model, model.runMenuEntry(1))

	if input, ok := dispatched.Input.(action.ChangeWork); dispatched.Action != action.AgentUnlink.ID || !ok || input.PR != 9 {
		t.Fatalf("dispatched %+v, want agent.unlink of #9", dispatched)
	}
}

func TestMarkingAClosedConversationUnseenAndSeenAsksItsMachine(t *testing.T) {
	var dispatched action.Invocation

	model := newInboxTestModel(t, nil, nil)
	recordingDispatcher(model, &dispatched)
	model.work = []work.Item{closedTestItem("w1", "conversation-one", time.Now())}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.invokeAction(action.InboxSeen.ID, action.Values{}))

	if dispatched.Action != "" || !strings.Contains(model.statusMessage, errAlreadySeen.Error()) {
		t.Fatalf("seen on a seen row dispatched %q with status %q, want only an error", dispatched.Action, model.statusMessage)
	}

	runHandler(t, model, model.invokeAction(action.InboxUnseen.ID, action.Values{}))

	input, ok := dispatched.Input.(action.ChangeWork)
	if dispatched.Action != action.AgentUnseen.ID || !ok || input.Item != "w1" || dispatched.At != action.Local() {
		t.Fatalf("dispatched %+v, want agent.unseen of w1 on this machine", dispatched)
	}

	if _, marked := model.inbox.FindUnseen("/" + inboxTestHarness + "/conversation-one"); marked {
		t.Fatal("unseen mark was kept in the local inbox state")
	}

	model.work[0].UnseenAt = time.Now().Unix()
	refreshInbox(model)
	assertInboxRows(t, model, map[string][]string{config.InboxDone: {"closed w1"}})
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.invokeAction(action.InboxSeen.ID, action.Values{}))

	if dispatched.Action != action.AgentSeen.ID {
		t.Fatalf("dispatched %+v, want agent.seen", dispatched)
	}
}

func TestMarkingARemoteClosedConversationUnseenGoesToItsMachine(t *testing.T) {
	var dispatched action.Invocation

	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: inboxTestHost, Host: inboxTestHost}}

	model := loaderTestModel(t, cfg, Dependencies{})
	recordingDispatcher(model, &dispatched)
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: time.Now(),
			Work: []work.Item{closedTestItem("w1", "conversation-one", time.Now())},
		}},
	}})
	model.displayMode = displayModeInbox
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.invokeAction(action.InboxUnseen.ID, action.Values{}))

	if dispatched.Action != action.AgentUnseen.ID || dispatched.At != action.Peer(inboxTestHost) {
		t.Fatalf("dispatched %+v, want agent.unseen on devbox", dispatched)
	}
}

func finishedTestItem(id string, turnAt time.Time) work.Item {
	item := closedTestItem(id, "conversation-"+id, turnAt)
	item.FinishedAt, item.FinishedBy = turnAt.Unix(), work.FinishedByUser

	return item
}

func TestFinishedConversationsAreListedWhenTheirSectionIsConfigured(t *testing.T) {
	model := newInboxTestModel(t, nil, nil)
	ofTask := finishedTestItem("w3", time.Now())
	ofTask.Task = "t1"
	model.work = []work.Item{finishedTestItem("w1", time.Now()), closedTestItem("w2", "conversation-w2", time.Now()), ofTask}

	model.config.Inbox.Sections = append(config.InboxSections(), config.InboxFinished)
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		config.InboxClosed:   {"closed w2"},
		config.InboxFinished: {"closed w1"},
	})
}

func TestFinishedRowsCollapseApartFromClosedRows(t *testing.T) {
	now := time.Now()
	model := newInboxTestModel(t, nil, nil)

	model.config.Inbox.Sections = append(config.InboxSections(), config.InboxFinished)
	model.config.Inbox.ClosedShown = 1

	unseen := closedTestItem("w3", "conversation-w3", now.Add(-time.Hour))
	unseen.UnseenAt = now.Unix()
	model.work = []work.Item{
		closedTestItem("w1", "conversation-w1", now.Add(-time.Minute)),
		closedTestItem("w2", "conversation-w2", now),
		unseen,
		closedTestItem("w4", "conversation-w4", now.Add(-2*time.Hour)),
		finishedTestItem("f1", now.Add(-time.Minute)),
		finishedTestItem("f2", now),
		finishedTestItem("f3", now.Add(-time.Hour)),
	}
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		config.InboxDone:     {"closed w3"},
		config.InboxClosed:   {"closed w2", "2 more"},
		config.InboxFinished: {"closed f2", "2 more"},
	})

	model.config.Inbox.Sections = []string{config.InboxFinished, config.InboxClosed}
	refreshInbox(model)
	selectInboxRow(t, model, "2 more")
	runHandler(t, model, model.handleEnter())

	assertInboxRows(t, model, map[string][]string{
		config.InboxClosed:   {"closed w2", "2 more"},
		config.InboxFinished: {"closed f2", "closed f1", "closed f3"},
	})
}

func TestEnterOnAFinishedConversationResumesItAndItsMenuOffersNoFinishOrSnooze(t *testing.T) {
	var dispatched action.Invocation

	model := newInboxTestModel(t, nil, nil)
	inboxState := model.deps.Inbox
	model.deps = testServices(Dependencies{RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
		dispatched = invocation

		return action.ResumedAgent{Item: "w1", Session: testLiveRef("$2", "api"), Pane: "%9"}, nil
	}})
	model.deps.Inbox = inboxState

	model.config.Inbox.Sections = append(config.InboxSections(), config.InboxFinished)
	model.config.Menu.Agent = []string{config.MenuGo, config.MenuSnooze, config.MenuPin, config.MenuFinish, config.MenuUnseen}
	model.work = []work.Item{finishedTestItem("w1", time.Now())}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.openRowMenu())

	labels := make([]string, 0, len(model.menu.entries))
	for _, entry := range model.menu.entries {
		labels = append(labels, entry.label)
	}

	if !slices.Equal(labels, []string{"go", config.MenuPin, config.MenuUnseen}) {
		t.Fatalf("menu = %q, want go, pin and unseen", labels)
	}

	updateTestModel(t, model, keyCode(tea.KeyEscape))
	runHandler(t, model, model.handleEnter())

	if input, ok := dispatched.Input.(action.ResumeAgent); dispatched.Action != action.AgentResume.ID || !ok || input.Item != "w1" {
		t.Fatalf("dispatched %+v, want agent.resume of w1", dispatched)
	}
}

func TestATaskRowShowsOnlyItsLedgerPullRequestAndRefusesLinking(t *testing.T) {
	var dispatched action.Invocation

	record := inboxTestRecord("t1", task.StateActive)
	record.PR = task.PullRequest{Number: 42, State: task.PRMerged, URL: ""}
	model := newInboxTestModel(t, []task.Live{inboxTestLive(record, "%1")}, nil)
	recordingDispatcher(model, &dispatched)

	ofTask := closedTestItem("w1", "conversation-one", time.Now())
	ofTask.Task = "t1"
	ofTask.PRs = []work.PRLink{{Number: 7, State: task.PROpen, URL: "", Issues: []int{3}, Source: work.LinkUser, CreatedAt: 0, EndedAt: 0}}
	model.work = []work.Item{ofTask}
	model.width, model.height = 160, 30
	refreshInbox(model)
	selectInboxRow(t, model, "t1")

	frame := stripANSI(model.renderFrame())
	if !strings.Contains(frame, "#42 merged") || !strings.Contains(frame, "ready to finish") || strings.Contains(frame, "#7") {
		t.Fatalf("frame does not show only the task's PR #42, ready to finish:\n%s", frame)
	}

	runHandler(t, model, model.invokeAction(action.InboxUnlink.ID, action.Values{}))

	if dispatched.Action != "" || !strings.Contains(model.statusMessage, errTaskPR.Error()) {
		t.Fatalf("unlink on a task row dispatched %q with status %q, want only an error", dispatched.Action, model.statusMessage)
	}
}

func TestEnterOnAClosedConversationResumesIt(t *testing.T) {
	var dispatched action.Invocation

	model := newInboxTestModel(t, nil, nil)
	inboxState := model.deps.Inbox
	model.deps = testServices(Dependencies{RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
		dispatched = invocation

		return action.ResumedAgent{Item: "w1", Session: testLiveRef("$2", "api"), Pane: "%9"}, nil
	}})
	model.deps.Inbox = inboxState
	model.work = []work.Item{closedTestItem("w1", "conversation-one", time.Now())}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	runHandler(t, model, model.handleEnter())

	input, ok := dispatched.Input.(action.ResumeAgent)
	if dispatched.Action != action.AgentResume.ID || !ok || input.Item != "w1" || dispatched.At != action.Local() {
		t.Fatalf("dispatched %+v, want agent.resume of w1 on this machine", dispatched)
	}
}

func TestClosedConversationMenuOffersFinishAndPinInsteadOfSnooze(t *testing.T) {
	model := newInboxTestModel(t, nil, nil)
	model.work = []work.Item{closedTestItem("w1", "conversation-one", time.Now())}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")
	model.config.Menu.Agent = []string{config.MenuGo, config.MenuSnooze, config.MenuPin, config.MenuUnpin, config.MenuFinish}

	runHandler(t, model, model.openRowMenu())

	labels := make([]string, 0, len(model.menu.entries))
	for _, entry := range model.menu.entries {
		labels = append(labels, entry.label)
	}

	if !slices.Equal(labels, []string{"go", config.MenuPin, config.MenuFinish}) {
		t.Fatalf("menu = %q, want go, pin and finish", labels)
	}
}

func TestAProjectShowsItsNewestClosedRowsAndExpandsTheRest(t *testing.T) {
	now := time.Now()
	model := newInboxTestModel(t, nil, nil)
	model.config.Inbox.ClosedShown = 3

	for index := range 5 {
		id := "w" + strconv.Itoa(index+1)
		model.work = append(model.work, closedTestItem(id, "conversation-"+id, now.Add(time.Duration(index)*time.Minute)))
	}

	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{config.InboxClosed: {"closed w5", "closed w4", "closed w3", "2 more"}})

	selectInboxRow(t, model, "2 more")
	runHandler(t, model, model.handleEnter())

	assertInboxRows(t, model, map[string][]string{
		config.InboxClosed: {"closed w5", "closed w4", "closed w3", "closed w2", "closed w1"},
	})
}

func TestNewTaskOnATaskRowClosesThePickerForItsProject(t *testing.T) {
	model := newInboxTestModel(t, []task.Live{inboxTestLive(inboxTestRecord("feature", task.StateActive), "%9")}, nil)
	selectInboxRow(t, model, "feature")

	_, cmd := model.invokeAction(action.InboxTask.ID, action.Values{})()

	if model.TaskProject() != "/work/api" || cmd == nil {
		t.Fatalf("TaskProject() = %q, cmd = %v, want /work/api and the picker closing", model.TaskProject(), cmd)
	}

	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("the picker stays open, want it closed")
	}
}

func TestNewTaskIsUnavailableOnAConversationOutsideAProject(t *testing.T) {
	model := newInboxTestModel(t, nil, nil)
	loose := closedTestItem("w1", "conversation-loose", time.Now())
	loose.Project = ""
	model.work = []work.Item{loose}
	refreshInbox(model)
	selectInboxRow(t, model, "closed w1")

	_, cmd := model.invokeAction(action.InboxTask.ID, action.Values{})()

	if model.TaskProject() != "" || cmd != nil {
		t.Fatalf("TaskProject() = %q, cmd = %v, want no project and the picker open", model.TaskProject(), cmd)
	}
}
