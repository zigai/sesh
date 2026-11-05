package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func recordedPopupHeight(t *testing.T, model *Model) int {
	t.Helper()
	model.View()

	for _, height := range model.FittingPopupHeights() {
		if height.Mode == string(model.displayMode) {
			return height.Height
		}
	}

	t.Fatal("render did not record the popup height")

	return 0
}

func appendEmptyPopupSessions(model *Model, count int) {
	for index := range count {
		model.sessions = append(model.sessions, session.Session{
			ID: fmt.Sprintf(
				"$%d",
				100+index,
			),
			Name: fmt.Sprintf("empty-%03d", index),
			Kind: session.KindLive,
		})
	}

	model.refreshDisplaySessions()
	model.filterSessions()
}

func TestAgentsPopupHeightIgnoresEmptySessionsAndTheirDivider(t *testing.T) {
	for _, count := range []int{0, 1, 100} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			model := newAgentsNavigationModel(t)
			model.mode = modePopup
			model.sessions = model.sessions[:2]
			appendEmptyPopupSessions(model, count)

			// Two session headings, three agents, input/footer, and frame borders.
			if got := recordedPopupHeight(t, model); got != 9 {
				t.Fatalf("height with %d empty sessions = %d, want 9", count, got)
			}

			if len(model.filtered) != 2+count {
				t.Fatal("height calculation removed empty sessions from the list")
			}
		})
	}
}

func TestAgentsPopupHeightCountsAllAgentsBeyondViewport(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.mode = modePopup
	model.sessions = model.sessions[:1]
	model.sessions[0].Agents = make([]session.AgentSession, 40)
	appendEmptyPopupSessions(model, 100)

	if got := recordedPopupHeight(t, model); got != 45 {
		t.Fatalf("height = %d, want one heading, 40 agents, and four chrome rows", got)
	}
}

func TestAgentsPopupHeightResetsWhenLastAgentsDisappear(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.mode = modePopup
	appendEmptyPopupSessions(model, 100)

	if got := recordedPopupHeight(t, model); got != 9 {
		t.Fatalf("initial height = %d, want 9", got)
	}

	for index := range model.sessions {
		model.sessions[index].Agents = nil
	}

	model.refreshDisplaySessions()
	model.filterSessions()
	model.fittingHeights[displayModeAgents] = 200

	if got := recordedPopupHeight(t, model); got != 4 {
		t.Fatalf("empty-only height = %d, want only four chrome rows", got)
	}

	model.sessions = nil
	model.refreshDisplaySessions()
	model.filterSessions()

	if got := recordedPopupHeight(t, model); got != 4 {
		t.Fatalf("no sessions height = %d, want only four chrome rows", got)
	}
}

func TestAgentsEmptySessionsRemainScrollableWithoutChangingFittingHeight(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.mode = modePopup
	appendEmptyPopupSessions(model, 100)
	want := recordedPopupHeight(t, model)
	model.Update(keyCtrlCode(tea.KeyUp))

	if row := model.selectedListRow(); row.isAgent() ||
		model.filtered[row.sessionIndex].Name != "empty-099" {
		t.Fatalf("control-up did not reach the last empty session: %+v", row)
	}

	if view := stripANSI(viewString(model.View())); !strings.Contains(view, "empty-099") {
		t.Fatal("last empty session did not scroll into the viewport")
	}

	if got := recordedPopupHeight(t, model); got != want {
		t.Fatalf("scrolling empty sessions changed fitting height from %d to %d", want, got)
	}

	model.Update(keyCode(tea.KeyDown))

	if !model.selectedListRow().isAgent() {
		t.Fatal("ordinary down did not return to agent-only navigation")
	}
}

func TestOtherPopupModesStillCountAllListRows(t *testing.T) {
	for _, mode := range []displayMode{displayModeSesh, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newAgentsNavigationModel(t)
			model.mode = modePopup
			model.displayMode = mode
			model.filtered = model.sessions
			model.View()
			want := len(model.listRows) + 4

			if got := recordedPopupHeight(t, model); got != want {
				t.Fatalf("%s height = %d, want all list rows plus chrome (%d)", mode, got, want)
			}
		})
	}
}

func TestAgentsPopupSearchDoesNotReplaceUnfilteredFittingHeight(t *testing.T) {
	model := newAgentsNavigationModel(t)
	model.mode = modePopup
	want := recordedPopupHeight(t, model)
	model.textInput.SetValue("alpha")
	model.filterSessions()

	if got := recordedPopupHeight(t, model); got != want {
		t.Fatalf("search changed fitting height from %d to %d", want, got)
	}
}

func TestAgentsPopupMoveDestinationsDoNotIncreaseFittingHeight(t *testing.T) {
	model := newTabMoveTestModel(t, displayModeAgents)
	model.config.AgentSessions.PopupShowEmptySessions = false
	model.refreshDisplaySessions()
	model.filterSessions()
	want := recordedPopupHeight(t, model)
	beginTestTabMove(t, model)

	foundEmptyDestination := false

	for index, row := range model.filtered {
		if row.Name == "empty" {
			foundEmptyDestination = model.tabMoveDestination(popupListRow{
				sessionIndex: index, agentIndex: parentAgentIndex,
			})
		}
	}

	if !foundEmptyDestination {
		t.Fatal("move mode did not offer the hidden empty destination")
	}

	if got := recordedPopupHeight(t, model); got != want {
		t.Fatalf(
			"revealing empty move destinations changed fitting height from %d to %d",
			want,
			got,
		)
	}

	model.Update(keyCode(tea.KeyEsc))
}
