package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

func workTestRecord(id string, state task.State) task.Record {
	record := task.EmptyRecord()
	record.ID = id
	record.Kind = task.KindProject
	record.State = state
	record.Project = "/work/alpha"
	record.Harness = "claude"
	record.Directory = "/work/trees/alpha-" + id
	record.Branch = "wt/main/" + id
	record.Base = "main"
	record.Title = id

	return record
}

func workTestLive(record task.Record, pane string) task.Live {
	return task.Live{
		Record: record, Present: pane != "", SessionID: "$1", SessionName: testSupportSessionAlphaName, WindowID: "@1", WindowIndex: 2,
		WindowName: record.Title, AgentPane: gotmux.PaneID(pane),
	}
}

func stalled(live task.Live) task.Live {
	live.Stalled = true

	return live
}

func newWorkTestModel(t *testing.T, tasks []task.Live, agents []session.AgentSession) *Model {
	t.Helper()

	model := newTestModel()
	model.sessions = []session.Session{{
		ID: "$1", Name: testSupportSessionAlphaName, Kind: session.KindLive, Origin: session.OriginLocal, Agents: agents, AgentTotal: len(agents),
	}}
	model.tasks = tasks
	model.displayMode = displayModeWork
	model.refreshDisplaySessions()
	model.filterSessions()

	return model
}

func workSectionsForTest(model *Model) map[string][]string {
	sections := make(map[string][]string)

	for _, row := range model.filtered {
		for _, agent := range row.Agents {
			sections[row.Name] = append(sections[row.Name], model.workTasks[workKey(row.RuntimeIdentity.ConnectionID, agent.PaneID)].live.Record.ID)
		}
	}

	return sections
}

func TestWorkSectionsFollowAgentStatusAndTaskState(t *testing.T) {
	merged := workTestRecord("merged", task.StateActive)
	merged.PR = task.PullRequest{Number: 4, State: task.PRMerged, URL: ""}

	tasks := []task.Live{
		workTestLive(workTestRecord("busy", task.StateActive), "%1"),
		workTestLive(workTestRecord("asking", task.StateActive), "%2"),
		workTestLive(workTestRecord("idle", task.StateActive), "%3"),
		workTestLive(workTestRecord("asleep", task.StateSleeping), "%4"),
		workTestLive(workTestRecord("interrupted-wake", task.StateWaking), "%5"),
		workTestLive(merged, "%6"),
		workTestLive(workTestRecord("starting", task.StateCreating), "%7"),
		workTestLive(workTestRecord("closing", task.StateFinishing), "%8"),
		stalled(workTestLive(workTestRecord("stuck-starting", task.StateCreating), "")),
		stalled(workTestLive(workTestRecord("stuck-closing", task.StateFinishing), "")),
	}
	agents := []session.AgentSession{
		{PaneID: "%1", Status: session.AgentStatusRunning},
		{PaneID: "%2", Status: session.AgentStatusWaiting},
		{PaneID: "%3", Status: session.AgentStatusIdle},
		{PaneID: "%6", Status: session.AgentStatusRunning},
	}

	got := workSectionsForTest(newWorkTestModel(t, tasks, agents))

	want := map[string][]string{
		"needs you": {"asking", "interrupted-wake", "stuck-starting", "stuck-closing"},
		"running":   {"busy"},
		"your turn": {"idle"},
		"sleeping":  {"asleep"},
		"done":      {"merged"},
	}
	for section, ids := range want {
		if strings.Join(got[section], ",") != strings.Join(ids, ",") {
			t.Errorf("section %q = %v, want %v", section, got[section], ids)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("sections = %v, want only %v", got, want)
	}
}

func TestWorkSectionHeadingsAreNotSelectable(t *testing.T) {
	model := newWorkTestModel(t, []task.Live{workTestLive(workTestRecord("t1", task.StateActive), "%1")}, nil)

	if model.rowSelectable(popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex}) {
		t.Fatal("section heading is selectable")
	}

	if found, ok := model.workTaskAt(model.selectedListRow()); !ok || found.live.Record.ID != "t1" {
		t.Fatalf("selected task = %q/%v, want t1", found.live.Record.ID, ok)
	}
}

func TestEnterOnTaskFocusesItsAgentPaneInTheOwningSession(t *testing.T) {
	model := newWorkTestModel(t, []task.Live{workTestLive(workTestRecord("t1", task.StateActive), "%9")}, nil)

	activated := ""

	deps := dependenciesForTest(model)
	deps.TmuxActivatePane = func(_ context.Context, sessionName string, paneID string) error {
		activated = sessionName + " " + paneID
		return nil
	}

	run := model.handleTaskEnter(model.selectedListRow())
	if run == nil {
		t.Fatal("enter on a task did nothing")
	}

	_, cmd := run()
	if msg, ok := cmd().(sessionActionResultMsg); !ok || msg.err != nil || activated != "alpha %9" {
		t.Fatalf("enter = %#v, activated %q; want alpha %%9", msg, activated)
	}
}

func TestEnterOnTaskWithoutWindowReportsIt(t *testing.T) {
	model := newWorkTestModel(t, []task.Live{workTestLive(workTestRecord("t1", task.StateActive), "")}, nil)

	run := model.handleTaskEnter(model.selectedListRow())
	if run == nil {
		t.Fatal("enter on a windowless task did nothing")
	}

	if _, cmd := run(); cmd != nil {
		t.Fatal("enter on a windowless task started an action")
	}

	if !strings.Contains(model.statusMessage, "t1") {
		t.Fatalf("status = %q, want it to name the task", model.statusMessage)
	}
}

func TestMergedTaskShowsRecordedPullRequestOnceGitHubNoLongerListsIt(t *testing.T) {
	record := workTestRecord("t1", task.StateActive)
	record.PR = task.PullRequest{Number: 42, State: task.PRMerged, URL: ""}
	model := newWorkTestModel(t, []task.Live{workTestLive(record, "%1")}, nil)
	model.width, model.height = 160, 30

	if frame := stripANSI(model.renderFrame()); !strings.Contains(frame, "#42") {
		t.Fatalf("frame lacks recorded PR:\n%s", frame)
	}
}

func TestCyclingThroughEmptyWorkKeepsTheSessionSelection(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{ID: "$1", Name: "first", Kind: session.KindLive},
		{ID: "$2", Name: "second", Kind: session.KindLive},
	}
	model.displayModes = []displayMode{displayModeSesh, displayModeWork}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.cursor = 1

	model.cycleDisplayMode()
	model.cycleDisplayMode()

	if got := model.filtered[model.cursor].ID; got != "$2" {
		t.Fatalf("selection after WORK = %s, want $2", got)
	}
}

func TestWorkListsRemoteTasksUnderTheirHostAndActsOnThem(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{{ID: "devbox", Host: "devbox"}}

	model := loaderTestModel(t, cfg, Dependencies{})
	model.sessions = []session.Session{{
		ID: "$1", Name: testSupportSessionAlphaName, Kind: session.KindLive, Origin: session.OriginLocal,
		Agents: []session.AgentSession{{PaneID: "%1", Status: session.AgentStatusIdle}}, AgentTotal: 1,
	}}
	model.tasks = []task.Live{workTestLive(workTestRecord("t1", task.StateActive), "%1")}

	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: time.Now(),
		Connections: []RemoteConnection{{
			ID: "devbox", Label: "devbox", Connected: true, LastSeen: time.Now(),
			Sessions: []session.Session{{
				ID: "$1", Name: "shop", Kind: session.KindLive,
				Agents: []session.AgentSession{{PaneID: "%1", Status: session.AgentStatusRunning}}, AgentTotal: 1,
			}},
			Tasks: []task.Live{workTestLive(workTestRecord("t7", task.StateActive), "%1")},
		}},
	}})

	model.displayMode = displayModeWork
	model.refreshDisplaySessions()
	model.filterSessions()

	got := workSectionsForTest(model)
	if strings.Join(got["your turn"], ",") != "t1" || strings.Join(got["running · devbox"], ",") != "t7" {
		t.Fatalf("sections = %v, want t1 here and t7 running on devbox", got)
	}

	model.cursor = slices.IndexFunc(model.filtered, func(row session.Session) bool { return row.Name == "running · devbox" })
	model.agentCursor = 0

	subject, ok := model.actionSubject(action.TaskSleep.Describe())
	if !ok || subject.At != action.Peer("devbox") || subject.Task != "t7" {
		t.Fatalf("subject = %+v, %v; want t7 on devbox", subject, ok)
	}
}
