package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func newAgentsNavigationModel(t *testing.T) *Model {
	t.Helper()

	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width, model.height = 80, 12
	model.initialAgentSelectionPending = false
	model.sessions = []session.Session{
		{
			ID: "$1", Name: "alpha", Kind: session.KindLive,
			Agents: []session.AgentSession{{PaneID: "%1"}, {PaneID: "%2"}},
		},
		{
			ID: "$2", Name: "beta", Kind: session.KindLive,
			Agents: []session.AgentSession{{PaneID: "%3"}},
		},
		{ID: "$3", Name: "empty-1", Kind: session.KindLive},
		{ID: "$4", Name: "empty-2", Kind: session.KindLive},
	}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})

	return model
}

func TestAgentsArrowKeysCycleOnlyAgentsInBothDirections(t *testing.T) {
	for _, showEmpty := range []bool{true, false} {
		model := newAgentsNavigationModel(t)
		model.config.AgentSessions.PopupShowEmptySessions = showEmpty
		model.refreshDisplaySessions()
		model.filterSessions()

		for _, direction := range []int{1, -1} {
			model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: 0})

			index := 0

			for range 12 {
				key := tea.KeyDown
				if direction < 0 {
					key = tea.KeyUp
				}

				model.Update(keyCode(key))

				index = (index + direction + 3) % 3

				selected := model.selectedListRow()
				if !selected.isAgent() {
					t.Fatalf("arrow selected tmux session instead of agent: %+v", selected)
				}

				want := []string{"%1", "%2", "%3"}[index]
				if got := model.filtered[selected.sessionIndex].Agents[selected.agentIndex].PaneID; got != want {
					t.Fatalf(
						"arrow cycle selected %s, want %s (show empty = %t)",
						got,
						want,
						showEmpty,
					)
				}
			}
		}
	}
}

func TestAgentsControlArrowsVisitEveryVisibleTmuxSession(t *testing.T) {
	for _, direction := range []int{1, -1} {
		model := newAgentsNavigationModel(t)
		index := 0

		for range 12 {
			key := tea.KeyDown
			if direction < 0 {
				key = tea.KeyUp
			}

			model.Update(keyCtrlCode(key))
			index = (index + direction + len(model.filtered)) % len(model.filtered)

			selected := model.selectedListRow()
			if selected.sessionIndex != index || selected.agentIndex != parentAgentIndex {
				t.Fatalf("control arrow selected %+v, want session heading %d", selected, index)
			}
		}
	}
}

func TestAgentsArrowsReturnFromEmptySessionToAgents(t *testing.T) {
	for _, direction := range []int{1, -1} {
		model := newAgentsNavigationModel(t)
		model.Update(keyCtrlCode(tea.KeyUp)) // Last empty session, via group navigation.

		if model.filtered[model.cursor].ID != "$4" {
			t.Fatal("control-up did not reach empty session")
		}

		key, want := tea.KeyDown, "%1"
		if direction < 0 {
			key, want = tea.KeyUp, "%3"
		}

		model.Update(keyCode(key))

		selected := model.selectedListRow()
		if !selected.isAgent() ||
			model.filtered[selected.sessionIndex].Agents[selected.agentIndex].PaneID != want {
			t.Fatalf("arrow from empty session selected %+v, want agent %s", selected, want)
		}
	}
}

func TestAgentsPageAndBoundaryNavigationSkipEmptySessions(t *testing.T) {
	model := newAgentsNavigationModel(t)
	for _, key := range []rune{tea.KeyEnd, tea.KeyPgDown, tea.KeyHome, tea.KeyPgUp} {
		model.Update(keyCode(key))

		if !model.selectedListRow().isAgent() {
			t.Fatalf("key %v selected empty tmux session", key)
		}
	}
}

func TestAgentsWithoutAnyAgentsDoNotCycleEmptySessionsWithArrows(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.sessions = model.sessions[2:]
	model.refreshDisplaySessions()
	model.filterSessions()
	model.selectListRow(popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex})

	for _, key := range []rune{tea.KeyUp, tea.KeyDown} {
		model.Update(keyCode(key))

		if model.cursor != 0 {
			t.Fatal("ordinary arrow cycled empty sessions when no agents exist")
		}
	}

	model.Update(keyCtrlCode(tea.KeyDown))

	if model.cursor != 1 {
		t.Fatal("control-down could not select an empty session")
	}

	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()

	if len(model.filtered) != 0 {
		t.Fatal("empty-only view did not become empty when disabled")
	}

	model.Update(keyCode(tea.KeyDown))
	model.Update(keyCtrlCode(tea.KeyDown))
	model.View()
}

func TestAgentsEmptySessionVisibilityDoesNotRemoveCatalogOrAffectSesh(t *testing.T) {
	model := newAgentsNavigationModel(t)
	if len(model.filtered) != 4 {
		t.Fatal("default Agents view hid empty sessions")
	}

	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()

	if len(model.filtered) != 2 || len(model.sessions) != 4 {
		t.Fatal("hiding empty sessions changed catalog or retained empty rows")
	}

	for _, row := range model.popupListRows() {
		if row.isSeparator() {
			t.Fatal("hidden empty sessions left a stray separator")
		}
	}

	for range 4 {
		model.Update(keyCtrlCode(tea.KeyDown))

		if len(model.filtered[model.cursor].Agents) == 0 {
			t.Fatal("control navigation reached a hidden session")
		}
	}

	model.displayMode = displayModeSesh
	model.refreshDisplaySessions()
	model.filterSessions()

	if len(model.filtered) != 4 {
		t.Fatal("Agents visibility setting leaked into Sesh view")
	}
}

func TestAgentsVisibilityTracksLastAgentDisappearingAndNewAgents(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.config.AgentSessions.PopupShowEmptySessions = false
	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$1",
		Agents:        []session.AgentSession{{PaneID: "%1"}}, Total: 1,
	}})

	if len(model.filtered) != 1 || model.filtered[0].ID != "$1" {
		t.Fatal("session remained visible after its last agent disappeared")
	}

	model.applyAgentSummaries([]session.AgentSummary{{
		TmuxSessionID: "$3",
		Agents:        []session.AgentSession{{PaneID: "%9"}}, Total: 1,
	}})

	if len(model.filtered) != 1 || model.filtered[0].ID != "$3" {
		t.Fatal("new agent in hidden session did not make it visible")
	}
}

func TestAgentsMoveModeOffersHiddenEmptyDestinationsAndRestoresVisibility(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()

	if slices.ContainsFunc(
		model.filtered,
		func(row session.Session) bool { return row.ID == "$3" },
	) {
		t.Fatal("empty session visible before move")
	}

	beginTestTabMove(t, model)
	selectTabMoveDestination(t, model, "$3")
	model.Update(keyCode(tea.KeyUp))

	if !model.tabMoveDestination(model.selectedListRow()) {
		t.Fatal("move-mode arrows did not select destination sessions")
	}

	model.Update(keyCode(tea.KeyEsc))

	if slices.ContainsFunc(
		model.filtered,
		func(row session.Session) bool { return row.ID == "$3" },
	) {
		t.Fatal("canceling move did not hide empty sessions again")
	}

	if model.selectedSessionIdentity().paneID != "%1" {
		t.Fatal("canceling move lost original agent selection")
	}
}
