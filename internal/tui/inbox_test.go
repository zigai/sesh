package tui

import (
	"context"
	"errors"
	"slices"
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
)

const inboxTestHost = "devbox"

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
	record.Harness = "claude"
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
	agent.Harness = "claude"
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
	})
	markViewed(model, "%5", now)
	markViewed(model, "%6", now)
	refreshInbox(model)

	assertInboxRows(t, model, map[string][]string{
		config.InboxDone:     {"fresh-new", "fresh-old", "broke", "stalled"},
		config.InboxWaiting:  {"asking"},
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
	snooze := inbox.Snooze{Key: conversationKey(agent), Until: time.Now().Add(time.Hour).Unix(), Since: time.Now().UnixNano()}

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
