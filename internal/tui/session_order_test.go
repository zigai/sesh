package tui

import (
	"reflect"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func sessionOrderTestRows() []session.Session {
	return []session.Session{
		{
			ID: "$7", Name: "agents", Kind: session.KindLive, Origin: session.OriginLocal,
			Agents: []session.AgentSession{
				{PaneID: "%7", RegistryID: "first", HarnessSession: "first"},
				{PaneID: "%8", RegistryID: "second", HarnessSession: "second"},
			},
		},
		{ID: "$4", Name: "work-z", Kind: session.KindLive, Origin: session.OriginLocal},
		{
			ID: "$1", Name: "lab", Kind: session.KindLive, Origin: session.OriginLocal,
			Agents: []session.AgentSession{{PaneID: "%1", HarnessSession: "lab"}},
		},
		{ID: "$6", Name: "priority-empty", Kind: session.KindLive, Origin: session.OriginLocal},
		{
			ID: "$5", Name: "other", Kind: session.KindLive, Origin: session.OriginLocal,
			Agents: []session.AgentSession{{PaneID: "%5", HarnessSession: "other"}},
		},
		{
			ID: "$2", Name: "work-a", Kind: session.KindLive, Origin: session.OriginLocal,
			Agents: []session.AgentSession{{PaneID: "%2", HarnessSession: "work"}},
		},
		{ID: "$3", Name: "empty-shell", Kind: session.KindLive, Origin: session.OriginLocal},
	}
}

func orderedSessionNames(rows []session.Session) []string {
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}

	return names
}

func newSessionOrderModel(t *testing.T, rules []config.SessionOrderRule) *Model {
	t.Helper()

	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width, model.height = 120, 30
	model.initialAgentSelectionPending = false
	model.config.AgentSessions.SessionOrder = rules
	model.sessions = sessionOrderTestRows()
	model.refreshDisplaySessions()
	model.filterSessions()

	return model
}

func TestAgentsSessionOrderRules(t *testing.T) {
	for _, test := range []struct {
		name  string
		rules []config.SessionOrderRule
		want  []string
	}{
		{
			name: "no rules preserves existing order",
			want: []string{"lab", "work-a", "other", "agents", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "last stays above empty sessions",
			rules: []config.SessionOrderRule{{Match: "agents", Position: "last"}},
			want:  []string{"lab", "work-a", "other", "agents", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "last moves an earlier active session to section bottom",
			rules: []config.SessionOrderRule{{Match: "work-a", Position: "last"}},
			want:  []string{"lab", "other", "agents", "work-a", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "glob first group retains internal order",
			rules: []config.SessionOrderRule{{Match: "work-*", Position: "first"}},
			want:  []string{"work-a", "lab", "other", "agents", "work-z", "empty-shell", "priority-empty"},
		},
		{
			name:  "last groups follow rule order",
			rules: []config.SessionOrderRule{{Match: "work-*", Position: "last"}, {Match: "agents", Position: "last"}},
			want:  []string{"lab", "other", "work-a", "agents", "empty-shell", "priority-empty", "work-z"},
		},
		{
			name: "first empty session stays in empty section",
			rules: []config.SessionOrderRule{
				{Match: "priority-empty", Position: "first"},
				{Match: "lab", Position: "first"},
			},
			want: []string{"lab", "work-a", "other", "agents", "priority-empty", "empty-shell", "work-z"},
		},
		{
			name:  "explicit normal group precedes unmatched within empty section",
			rules: []config.SessionOrderRule{{Match: "work-z", Position: "normal"}},
			want:  []string{"lab", "work-a", "other", "agents", "work-z", "empty-shell", "priority-empty"},
		},
		{
			name:  "explicit normal group precedes unmatched within active section",
			rules: []config.SessionOrderRule{{Match: "other", Position: "normal"}},
			want:  []string{"other", "lab", "work-a", "agents", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "first matching rule wins",
			rules: []config.SessionOrderRule{{Match: "*", Position: "normal"}, {Match: "agents", Position: "last"}},
			want:  []string{"lab", "work-a", "other", "agents", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "case sensitive",
			rules: []config.SessionOrderRule{{Match: "Agents", Position: "last"}},
			want:  []string{"lab", "work-a", "other", "agents", "empty-shell", "work-z", "priority-empty"},
		},
		{
			name:  "nonmatching rule leaves order unchanged",
			rules: []config.SessionOrderRule{{Match: "missing-*", Position: "first"}},
			want:  []string{"lab", "work-a", "other", "agents", "empty-shell", "work-z", "priority-empty"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := sessionOrderTestRows()
			sortAgentsModeSessions(rows, test.rules)

			if got := orderedSessionNames(rows); !slices.Equal(got, test.want) {
				t.Fatalf("session order = %v, want %v", got, test.want)
			}

			for _, row := range rows {
				if row.Name == "agents" &&
					(len(row.Agents) != 2 || row.Agents[0].PaneID != "%7" || row.Agents[1].PaneID != "%8") {
					t.Fatal("session ordering changed the agents inside its group")
				}
			}
		})
	}
}

func TestAgentsSessionOrderTiesKeepDefaultOrderAndAreStable(t *testing.T) {
	rows := sessionOrderTestRows()
	want := slices.Clone(rows)
	slices.SortStableFunc(want, compareAgentsModeSessions)

	for range 3 {
		sortAgentsModeSessions(rows, []config.SessionOrderRule{{Match: "*", Position: "first"}})

		if !reflect.DeepEqual(rows, want) {
			t.Fatal("equal rule groups did not retain the default ordering")
		}
	}
}

func TestAgentsPinnedLastGroupRendersAboveEmptySessionDivider(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "agents", Position: "last"}})
	rows := slices.Clone(model.popupListRows())
	group := 3
	start := slices.Index(rows, popupListRow{sessionIndex: group, agentIndex: parentAgentIndex})

	want := []popupListRow{
		{sessionIndex: group, agentIndex: parentAgentIndex},
		{sessionIndex: group, agentIndex: 0},
		{sessionIndex: group, agentIndex: 1},
		{sessionIndex: group + 1, agentIndex: separatorAgentIndex},
		{sessionIndex: group + 1, agentIndex: parentAgentIndex},
	}
	if start < 0 || start+len(want) > len(rows) ||
		!slices.Equal(rows[start:start+len(want)], want) {
		t.Fatalf("pinned group must stay together above the empty-session divider: %+v", rows)
	}

	dividers := 0

	for _, row := range rows {
		if row.isSeparator() {
			dividers++

			if model.rowSelectable(row) {
				t.Fatal("separator became selectable")
			}
		}
	}

	if dividers != 1 {
		t.Fatalf("divider count = %d, want one between active and empty sessions", dividers)
	}

	for _, width := range []int{48, 120} {
		model.width = width
		assertVisualViewport(t, viewString(model.View()), width, model.height)
	}
}

func TestAgentsPinnedEmptySessionStaysLastButVisibilityStillApplies(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "agents", Position: "last"}})
	model.sessions[0].Agents = nil
	model.refreshDisplaySessions()
	model.filterSessions()

	if model.filtered[len(model.filtered)-1].Name != "agents" {
		t.Fatal("empty pinned session is not last")
	}

	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()

	if slices.ContainsFunc(
		model.filtered,
		func(row session.Session) bool { return row.Name == "agents" },
	) {
		t.Fatal("ordering overrode empty-session visibility")
	}
}

func TestAgentsSessionOrderDoesNotChangeCatalogOrSeshView(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "agents", Position: "last"}})
	if !reflect.DeepEqual(model.sessions, sessionOrderTestRows()) {
		t.Fatal("session ordering mutated source catalog")
	}

	model.displayMode = displayModeSesh
	model.refreshDisplaySessions()
	model.filterSessions()

	if !slices.Equal(orderedSessionNames(model.filtered), orderedSessionNames(model.sessions)) {
		t.Fatal("Agents ordering rules affected Sesh view")
	}
}

func TestAgentsSessionOrderPersistsThroughFilteringAndRefresh(t *testing.T) {
	model := newSessionOrderModel(t, nil)
	model.sessions[0].ID = "$0"
	model.refreshDisplaySessions()
	model.filterSessions()

	for i, row := range model.filtered {
		if row.Name == "agents" {
			model.selectListRow(popupListRow{sessionIndex: i, agentIndex: 1})
		}
	}

	model.config.AgentSessions.SessionOrder = []config.SessionOrderRule{
		{Match: "agents", Position: "last"},
	}

	summaries := make([]session.AgentSummary, 0, len(model.sessions))
	for _, row := range model.sessions {
		summaries = append(
			summaries,
			session.AgentSummary{TmuxSessionID: row.ID, Agents: row.Agents, Total: len(row.Agents)},
		)
	}

	model.applyAgentSummaries(summaries)

	if selected := model.selectedSessionIdentity(); selected.paneID != "%8" ||
		model.filtered[model.cursor].Name != "agents" || model.cursor != 3 {
		t.Fatalf("refresh lost selected pinned agent: %+v", selected)
	}

	model.config.AgentSessions.SessionOrder = []config.SessionOrderRule{
		{Match: "work-a", Position: "last"},
	}
	model.refreshDisplaySessions()
	model.textInput.SetValue("work")
	model.filterSessions()

	if got := orderedSessionNames(
		model.filtered,
	); !slices.Equal(
		got,
		[]string{"work-a", "work-z"},
	) {
		t.Fatalf("search reordered rules: %v", got)
	}
}

func TestAgentsSessionOrderDrivesBothNavigationModesAndMoveSelection(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "agents", Position: "last"}})
	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})
	model.Update(keyCode(tea.KeyUp))

	if got := model.selectedSessionIdentity().paneID; got != "%8" {
		t.Fatalf("up wrap = %s, want last pinned agent", got)
	}

	model.Update(keyCode(tea.KeyDown))

	if got := model.selectedSessionIdentity().paneID; got != "%1" {
		t.Fatalf("down wrap = %s, want first agent", got)
	}

	model.Update(keyCtrlCode(tea.KeyUp))

	if model.filtered[model.cursor].Name != "priority-empty" || model.selectedListRow().isAgent() {
		t.Fatal("control-up did not reach last empty session heading")
	}

	model.selectListRow(popupListRow{sessionIndex: 2, agentIndex: 0})
	model.Update(keyCtrlCode(tea.KeyDown))

	if model.filtered[model.cursor].Name != "agents" {
		t.Fatal("control-down did not reach pinned session above empty section")
	}

	model.Update(keyCtrlCode(tea.KeyDown))

	if model.filtered[model.cursor].Name != "empty-shell" {
		t.Fatal("control-down did not reach empty section after pinned session")
	}

	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})
	beginTestTabMove(t, model)
	model.Update(keyCode(tea.KeyEnd))

	if model.filtered[model.cursor].Name != "priority-empty" ||
		!model.tabMoveDestination(model.selectedListRow()) {
		t.Fatal("move mode did not keep empty destinations after active sessions")
	}

	model.Update(keyCode(tea.KeyUp))
	model.Update(keyCode(tea.KeyUp))
	model.Update(keyCode(tea.KeyUp))

	if model.filtered[model.cursor].Name != "agents" ||
		!model.tabMoveDestination(model.selectedListRow()) {
		t.Fatal("move mode did not keep pinned session above empty destinations")
	}

	model.Update(keyCode(tea.KeyEsc))
}

func TestAgentsSessionOrderTracksAgentPresenceOnRefresh(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "work-a", Position: "last"}})

	for _, present := range []bool{false, true, false} {
		summaries := make([]session.AgentSummary, 0, len(model.sessions))

		for _, row := range sessionOrderTestRows() {
			if row.Name == "work-a" && !present {
				row.Agents = nil
			}

			summaries = append(summaries, session.AgentSummary{
				TmuxSessionID: row.ID, Agents: row.Agents, Total: len(row.Agents),
			})
		}

		model.applyAgentSummaries(summaries)

		want := []string{
			"lab",
			"other",
			"agents",
			"empty-shell",
			"work-z",
			"priority-empty",
			"work-a",
		}
		if present {
			want = []string{
				"lab",
				"other",
				"agents",
				"work-a",
				"empty-shell",
				"work-z",
				"priority-empty",
			}
		}

		if got := orderedSessionNames(model.filtered); !slices.Equal(got, want) {
			t.Fatalf("agent present=%t: order = %v, want %v", present, got, want)
		}
	}
}

func TestAgentsSessionOrderPreservesSectionsWhenMoveRevealsEmptySessions(t *testing.T) {
	model := newSessionOrderModel(t, []config.SessionOrderRule{{Match: "work-a", Position: "last"}})
	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()

	wantActive := []string{"lab", "other", "agents", "work-a"}
	if got := orderedSessionNames(model.filtered); !slices.Equal(got, wantActive) {
		t.Fatalf("hidden empty sessions: order = %v, want %v", got, wantActive)
	}

	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})
	beginTestTabMove(t, model)

	wantAll := []string{
		"lab",
		"other",
		"agents",
		"work-a",
		"empty-shell",
		"work-z",
		"priority-empty",
	}
	if got := orderedSessionNames(model.filtered); !slices.Equal(got, wantAll) {
		t.Fatalf("move revealed empty sessions: order = %v, want %v", got, wantAll)
	}

	model.Update(keyCode(tea.KeyEsc))

	if got := orderedSessionNames(model.filtered); !slices.Equal(got, wantActive) {
		t.Fatalf("cancel move: order = %v, want %v", got, wantActive)
	}
}
