package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestAgentPositionOrder(t *testing.T) {
	agents := []session.AgentSession{
		{HarnessSession: "A unknown"},
		{HarnessSession: "B window 10", PositionKnown: true, WindowIndex: 10},
		{HarnessSession: "C right", PositionKnown: true, WindowIndex: 2, PaneLeft: 40},
		{HarnessSession: "D lower left", PositionKnown: true, WindowIndex: 2, PaneTop: 12},
		{HarnessSession: "E upper left", PositionKnown: true, WindowIndex: 2},
		{HarnessSession: "F window zero", PositionKnown: true},
	}
	sortAgentSessions(agents, agentSortPosition)
	assertAgentNames(t, agents, []string{"F window zero", "E upper left", "D lower left", "C right", "B window 10", "A unknown"})
}

func TestAgentAttentionOrder(t *testing.T) {
	now := time.Now()
	agents := []session.AgentSession{
		{HarnessSession: "unknown", Status: session.AgentStatusUnknown},
		{HarnessSession: "working", Status: session.AgentStatusRunning},
		{HarnessSession: "idle", Status: session.AgentStatusIdle},
		{HarnessSession: "interrupted", Status: session.AgentStatusInterrupted},
		{HarnessSession: "failed", Status: session.AgentStatusFailed},
		{HarnessSession: "waiting no time", Status: session.AgentStatusWaiting},
		{HarnessSession: "waiting new", Status: session.AgentStatusWaiting, StatusSince: now},
		{HarnessSession: "waiting old right", Status: session.AgentStatusWaiting, StatusSince: now.Add(-time.Hour), PositionKnown: true, PaneLeft: 20},
		{HarnessSession: "waiting old left", Status: session.AgentStatusWaiting, StatusSince: now.Add(-time.Hour), PositionKnown: true},
	}
	sortAgentSessions(agents, agentSortAttention)
	assertAgentNames(t, agents, []string{"waiting old left", "waiting old right", "waiting new", "waiting no time", "failed", "interrupted", "idle", "working", "unknown"})
}

func TestAgentSortCyclingPreservesSelectionAndSearch(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName, Agents: []session.AgentSession{
		{RegistryID: "z", HarnessSession: "Zebra", PositionKnown: true},
		{RegistryID: "a", HarnessSession: "Alpha", PositionKnown: true, PaneLeft: 30},
	}}}
	model.textInput.SetValue(testSupportSessionDevName)
	model.refreshDisplaySessions()
	model.filterSessions()
	model.cursor = 0
	model.agentCursor = 0

	assertAgentNames(t, model.filtered[0].Agents, []string{"Zebra", "Alpha"})

	updateTestModel(t, model, keyCtrl('s'))

	if model.dialog != dialogNone || model.agentSort != agentSortName || model.textInput.Value() != testSupportSessionDevName {
		t.Fatal("cycling to Name changed dialog or reset search")
	}

	assertAgentNames(t, model.filtered[0].Agents, []string{"Alpha", "Zebra"})

	if model.selectedSessionIdentity().agentID != "z" || model.agentCursor != 1 {
		t.Fatal("selection moved to a different agent after sorting")
	}

	assertAgentNames(t, model.sessions[0].Agents, []string{"Zebra", "Alpha"})

	if model.config.AgentSessions.PopupSort != "position" {
		t.Fatal("popup choice changed the configured default")
	}

	updateTestModel(t, model, keyCtrl('s'))

	if model.dialog != dialogNone || model.agentSort != agentSortAttention {
		t.Fatal("second ctrl+s did not cycle to Attention")
	}

	updateTestModel(t, model, keyCtrl('s'))

	if model.dialog != dialogNone || model.agentSort != agentSortPosition {
		t.Fatal("third ctrl+s did not cycle back to Position")
	}

	assertAgentNames(t, model.filtered[0].Agents, []string{"Zebra", "Alpha"})
}

func TestAgentSortChoiceSurvivesSummaryRefresh(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName}}
	model.setAgentSort(agentSortAttention)

	now := time.Now()
	summaries := []session.AgentSummary{{TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Agents: []session.AgentSession{
		{RegistryID: "a", HarnessSession: "Alpha", Status: session.AgentStatusIdle, StatusSince: now},
		{RegistryID: "z", HarnessSession: "Zebra", Status: session.AgentStatusWaiting, StatusSince: now},
	}}}
	model.applyAgentSummaries(summaries)
	model.agentCursor = 0
	assertAgentNames(t, model.filtered[0].Agents, []string{"Zebra", "Alpha"})
	summaries[0].Agents[0].Status = session.AgentStatusWaiting
	summaries[0].Agents[0].StatusSince = now.Add(-time.Hour)
	model.applyAgentSummaries(summaries)
	assertAgentNames(t, model.filtered[0].Agents, []string{"Alpha", "Zebra"})

	if model.agentSort != agentSortAttention || model.selectedSessionIdentity().agentID != "z" {
		t.Fatal("refresh lost the sort choice or selected agent")
	}
}

func TestAgentPositionRefreshPreservesSelectionAndRemoteRows(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{ID: "$1", Name: testSupportSessionDevName, Agents: []session.AgentSession{
			{RegistryID: "a", PaneID: "%1", HarnessSession: "Alpha", PositionKnown: true},
			{RegistryID: "z", PaneID: "%2", HarnessSession: "Zebra", PositionKnown: true, PaneLeft: 20},
		}},
		{ID: "$1", Name: "remote", Origin: session.OriginSSH, Agents: []session.AgentSession{
			{RegistryID: "remote", PaneID: "%1", PositionKnown: true, WindowIndex: 9},
		}},
	}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.cursor = 0
	model.agentCursor = 0
	model.applyAgentPositions(agentPositionsMsg{ready: true, panes: []PaneLocation{
		{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", WindowIndex: "0", PaneLeft: "20", PaneTop: "0"},
		{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%2", WindowIndex: "0", PaneLeft: "0", PaneTop: "0"},
	}})
	assertAgentNames(t, model.filtered[0].Agents, []string{"Zebra", "Alpha"})

	if model.selectedSessionIdentity().agentID != "a" || model.sessions[1].Agents[0].WindowIndex != 9 {
		t.Fatal("geometry refresh lost selection or overwrote remote geometry")
	}

	model.applyAgentPositions(agentPositionsMsg{})
	assertAgentNames(t, model.filtered[0].Agents, []string{"Zebra", "Alpha"})
}

func TestAgentPositionOrderRemainsStableAcrossActivityUpdates(t *testing.T) {
	const (
		nameFirst   = "Alpha"
		nameLast    = "Zebra"
		nameUpdated = "A new title"
	)

	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName}}
	summaries := []session.AgentSummary{{TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Agents: []session.AgentSession{
		{RegistryID: "a", PaneID: "%1", PanePID: 101, HarnessSession: nameFirst, Status: session.AgentStatusRunning},
		{RegistryID: "z", PaneID: "%2", PanePID: 102, HarnessSession: nameLast, Status: session.AgentStatusRunning},
	}}}
	positions := agentPositionsMsg{ready: true, panes: []PaneLocation{
		{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", PanePID: 101, WindowIndex: "2", PaneLeft: "20", PaneTop: "0"},
		{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%2", PanePID: 102, WindowIndex: "2", PaneLeft: "0", PaneTop: "0"},
	}}

	updateTestModel(t, model, agentSummariesMsg{summaries: summaries, revision: 1})
	updateTestModel(t, model, positions)
	model.cursor = 0
	model.agentCursor = 0
	assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameFirst})

	for revision := uint64(2); revision <= 5; revision++ {
		summaries[0].Agents[0].StatusSince = time.Unix(int64(revision), 0)
		summaries[0].Agents[0].Status = session.AgentStatusWaiting
		summaries[0].Agents[0].HarnessSession = nameUpdated
		updateTestModel(t, model, agentSummariesMsg{summaries: summaries, revision: revision})
		assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameUpdated})

		if model.selectedSessionIdentity().agentID != "z" || model.agentCursor != 0 {
			t.Fatal("activity refresh moved the selected agent")
		}

		updated := model.filtered[0].Agents[1]
		if updated.Status != session.AgentStatusWaiting || !updated.StatusSince.Equal(time.Unix(int64(revision), 0)) {
			t.Fatal("stable position order prevented fresh activity from being displayed")
		}

		updateTestModel(t, model, positions)
		assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameUpdated})
	}

	updateTestModel(t, model, agentPositionsMsg{})
	assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameUpdated})

	positions.panes[0].PaneLeft = "0"
	positions.panes[1].PaneLeft = "20"
	updateTestModel(t, model, positions)
	assertAgentNames(t, model.filtered[0].Agents, []string{nameUpdated, nameLast})
	updateTestModel(t, model, agentSummariesMsg{summaries: summaries, revision: 6})
	assertAgentNames(t, model.filtered[0].Agents, []string{nameUpdated, nameLast})

	if model.selectedSessionIdentity().agentID != "z" || model.agentCursor != 1 {
		t.Fatal("pane movement lost the selected agent")
	}

	for _, agent := range summaries[0].Agents {
		if agent.PositionKnown {
			t.Fatal("model wrote pane geometry into the broker's summary")
		}
	}
}

func TestAgentPositionOrderUsesGeometryOnInitialLoad(t *testing.T) {
	const (
		nameFirst = "Alpha"
		nameLast  = "Zebra"
	)

	model := newTestModelWithDeps(Dependencies{
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{Sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}}, Status: session.StatusComplete}, nil
		},
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) {
			return []session.AgentSummary{{TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName, Agents: []session.AgentSession{
				{RegistryID: "a", PaneID: "%1", HarnessSession: nameFirst},
				{RegistryID: "z", PaneID: "%2", HarnessSession: nameLast},
			}}}, nil
		},
		TmuxPaneLocations: func(context.Context) ([]PaneLocation, error) {
			return []PaneLocation{
				{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%1", WindowIndex: "0", PaneLeft: "20", PaneTop: "0"},
				{SessionID: "$1", SessionName: testSupportSessionDevName, PaneID: "%2", WindowIndex: "0", PaneLeft: "0", PaneTop: "0"},
			}, nil
		},
	})
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents

	if err := model.loadSessions(); err != nil {
		t.Fatal(err)
	}

	assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameFirst})
}

func TestAgentPositionRefreshHonorsNewPaneEvidence(t *testing.T) {
	const (
		nameFirst = "Alpha"
		nameLast  = "Zebra"
	)

	for _, testCase := range []struct {
		name  string
		agent session.AgentSession
	}{
		{name: "agent moved to another pane", agent: session.AgentSession{RegistryID: "z", PaneID: "%3", PanePID: 102, HarnessSession: nameLast}},
		{name: "pane process replaced", agent: session.AgentSession{RegistryID: "z", PaneID: "%2", PanePID: 103, HarnessSession: nameLast}},
		{name: "new agent in same pane", agent: session.AgentSession{RegistryID: "new", PaneID: "%2", PanePID: 102, HarnessSession: nameLast}},
		{name: "new geometry supplied", agent: session.AgentSession{RegistryID: "z", PaneID: "%2", PanePID: 102, HarnessSession: nameLast, PositionKnown: true, PaneLeft: 40}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model := newTestModel()
			t.Cleanup(model.Close)
			model.displayMode = displayModeAgents
			model.sessions = []session.Session{{ID: "$1", Name: testSupportSessionDevName, Agents: []session.AgentSession{
				{RegistryID: "a", PaneID: "%1", PanePID: 101, HarnessSession: nameFirst, PositionKnown: true, PaneLeft: 20},
				{RegistryID: "z", PaneID: "%2", PanePID: 102, HarnessSession: nameLast, PositionKnown: true},
			}}}
			model.refreshDisplaySessions()
			model.filterSessions()
			assertAgentNames(t, model.filtered[0].Agents, []string{nameLast, nameFirst})
			updateTestModel(t, model, agentSummariesMsg{summaries: []session.AgentSummary{{
				TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName,
				Agents: []session.AgentSession{
					{RegistryID: "a", PaneID: "%1", PanePID: 101, HarnessSession: nameFirst},
					testCase.agent,
				},
			}}})
			assertAgentNames(t, model.filtered[0].Agents, []string{nameFirst, nameLast})

			updateTestModel(t, model, agentSummariesMsg{summaries: []session.AgentSummary{{
				TmuxSessionID: "$1", TmuxSessionName: testSupportSessionDevName,
				Agents: []session.AgentSession{testCase.agent},
			}}})
			assertAgentNames(t, model.filtered[0].Agents, []string{nameLast})
		})
	}
}

func checkAgentSortConfigAndFooter(t *testing.T, value string) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.AgentSessions.PopupSort = value
	cfg.Settings.DisplayModes = []string{"agents"}
	cfg.Settings.ShowFooter = false

	model, err := newModelWithDeps(cfg, &session.State{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(model.Close)

	model.width, model.height = 100, 24
	for _, width := range []int{20, 40, 80} {
		footer := model.renderModeFooter(width, false)
		if lipgloss.Width(footer) != width {
			t.Fatalf("footer width = %d, want %d", lipgloss.Width(footer), width)
		}
	}

	if !strings.Contains(model.renderModeFooter(80, false), "sort: "+parseAgentSort(value).label()) {
		t.Fatal("single-mode footer omitted the configured order")
	}

	layout := model.popupMouseLayout()
	updateTestModel(t, model, mouseClick(layout.bodyX+layout.layout.bodyWidth-3, layout.footerY, tea.MouseLeft))

	initialSort := parseAgentSort(value)
	expectedChoices := agentSortChoices()
	expectedIndex := 0

	for i, choice := range expectedChoices {
		if choice == initialSort {
			expectedIndex = i
			break
		}
	}

	expectedNextSort := expectedChoices[(expectedIndex+1)%len(expectedChoices)]
	if model.dialog != dialogNone || model.agentSort != expectedNextSort {
		t.Fatalf("clicking sort indicator = %v, want cycled to %v", model.agentSort, expectedNextSort)
	}
}

func TestAgentSortConfigAndFooter(t *testing.T) {
	for _, value := range []string{"", "position", "name", " Attention "} {
		t.Run(value, func(t *testing.T) {
			checkAgentSortConfigAndFooter(t, value)
		})
	}
}

func assertAgentNames(t *testing.T, agents []session.AgentSession, want []string) {
	t.Helper()

	got := make([]string, 0, len(agents))
	for _, agent := range agents {
		got = append(got, agent.HarnessSession)
	}

	if !slices.Equal(got, want) {
		t.Fatalf("agent order = %v, want %v", got, want)
	}
}

func TestAgentSortCycleWrapsThroughChoices(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents

	expected := []agentSort{agentSortName, agentSortAttention, agentSortPosition, agentSortName}
	for _, want := range expected {
		if !model.cycleAgentSort() {
			t.Fatal("cycleAgentSort() = false, want true")
		}

		if model.agentSort != want {
			t.Fatalf("agentSort = %v, want %v", model.agentSort, want)
		}
	}
}

func TestAgentSortBadgeStylingMatchesFooterShortcuts(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents

	badge := model.renderAgentSortBadge()
	if !strings.HasPrefix(badge, ansiFooterKey+"ctrl+s"+ansiFooterKeyReset) {
		t.Fatalf("badge key missing footer shortcut key style: %q", badge)
	}

	if !strings.HasSuffix(badge, ansiFooterDesc+"sort: position"+ansiReset) {
		t.Fatalf("badge desc missing footer description style: %q", badge)
	}

	if plain := stripANSI(badge); plain != "ctrl+s sort: position" {
		t.Fatalf("badge plain text = %q, want %q", plain, "ctrl+s sort: position")
	}

	model.config.Keys.AgentSort = ""
	badgeNoKey := model.renderAgentSortBadge()
	wantNoKey := ansiFooterDesc + "sort: position" + ansiReset

	if badgeNoKey != wantNoKey {
		t.Fatalf("unbound key badge = %q, want %q", badgeNoKey, wantNoKey)
	}
}

func TestHiddenAgentSortBadgeHasNoMouseTarget(t *testing.T) {
	model := newTestModel()
	t.Cleanup(model.Close)
	model.displayMode = displayModeAgents
	model.width, model.height = 80, 24
	layout := model.popupMouseLayout()
	updateTestModel(t, model, mouseClick(layout.bodyX+layout.layout.bodyWidth-3, layout.footerY, tea.MouseLeft))

	if model.dialog != dialogNone || model.agentSort != agentSortPosition {
		t.Fatal("clicking the mode badge changed sort or opened dialog")
	}
}

func TestAgentPositionPollingContinuesWithQuietBroker(t *testing.T) {
	called := false
	model := newTestModelWithDeps(Dependencies{
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) { return nil, nil },
		TmuxPaneLocations: func(context.Context) ([]PaneLocation, error) {
			called = true
			return []PaneLocation{{PaneID: "%1"}}, nil
		},
	})
	t.Cleanup(model.Close)
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.agentSummaryWatchStarted = true

	_, command := model.Update(agentPositionTickMsg{})
	if command == nil {
		t.Fatal("a quiet realtime broker disabled position polling")
	}

	message := command()

	positions, ok := message.(agentPositionsMsg)
	if !ok || !positions.ready || !called {
		t.Fatalf("position poll returned %T without loading geometry", message)
	}

	_, next := model.Update(positions)
	if next == nil {
		t.Fatal("position refresh did not schedule its next tick")
	}
}
