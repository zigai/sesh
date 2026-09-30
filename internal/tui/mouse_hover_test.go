package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

const (
	hoverEarlierProject = "earlier-project"
	hoverMiddleProject  = "middle-project"
	hoverLaterProject   = "later-project"
)

func TestMouseHoverSessionHeadingKeepsAgentRowsInPlace(t *testing.T) {
	tests := []struct {
		name         string
		sessions     []session.Session
		height       int
		listTop      int
		cursor       int
		agentCursor  int
		hoverRow     int
		hoverSession int
		visibleNames []string
	}{
		{
			name: "repeated hover of partially visible heading",
			sessions: []session.Session{
				{Name: hoverEarlierProject, Agents: []session.AgentSession{{Harness: testSupportHarnessCodex, HarnessSession: "earlier-agent"}}},
				{Name: hoverMiddleProject, Agents: []session.AgentSession{{Harness: testSupportHarnessCodex, HarnessSession: "middle-agent"}}},
				{Name: hoverLaterProject, Agents: []session.AgentSession{
					{Harness: testSupportHarnessCodex, HarnessSession: "later-first"},
					{Harness: testSupportHarnessCodex, HarnessSession: "later-second"},
				}},
			},
			height:       8,
			cursor:       2,
			agentCursor:  0,
			hoverRow:     2,
			hoverSession: 2,
			visibleNames: []string{hoverMiddleProject, "middle-agent", hoverLaterProject, "later-first"},
		},
		{
			name: "crowded heading at final visible row",
			sessions: []session.Session{
				{Name: hoverEarlierProject, Agents: hoverAgents("earlier-first", "earlier-second", "earlier-third")},
				{Name: hoverMiddleProject, Agents: hoverAgents("middle-first", "middle-second", "middle-third")},
				{Name: hoverLaterProject, Agents: hoverAgents("later-first", "later-second", "later-third")},
			},
			height:       9,
			agentCursor:  0,
			hoverRow:     4,
			hoverSession: 1,
			visibleNames: []string{hoverEarlierProject, "earlier-first", "earlier-second", "earlier-third", hoverMiddleProject},
		},
		{
			name: "heading below a partially visible different session",
			sessions: []session.Session{
				{Name: hoverEarlierProject, Agents: hoverAgents("earlier-first", "earlier-second", "earlier-third")},
				{Name: hoverMiddleProject, Agents: hoverAgents("middle-first", "middle-second", "middle-third")},
				{Name: hoverLaterProject, Agents: hoverAgents("later-first", "later-second", "later-third")},
			},
			height:       9,
			listTop:      2,
			agentCursor:  1,
			hoverRow:     2,
			hoverSession: 1,
			visibleNames: []string{"earlier-second", "earlier-third", hoverMiddleProject, "middle-first", "middle-second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newTestModel()
			model.mode = modePopup
			model.displayMode = displayModeAgents
			model.width = 80
			model.height = tt.height
			model.filtered = tt.sessions
			model.listTop = tt.listTop
			model.cursor = tt.cursor
			model.agentCursor = tt.agentCursor

			layout := model.popupMouseLayout()
			if layout.layout.listHeight != len(tt.visibleNames) {
				t.Fatalf("list height = %d, want %d", layout.layout.listHeight, len(tt.visibleNames))
			}

			assertHoverVisibleRows(t, model, tt.visibleNames)

			for motion := 1; motion <= 4; motion++ {
				model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+tt.hoverRow))
				if model.cursor != tt.hoverSession || model.agentCursor != parentAgentIndex {
					t.Errorf("motion %d selected session/agent = %d/%d, want heading %d/-1", motion, model.cursor, model.agentCursor, tt.hoverSession)
				}

				assertHoverVisibleRows(t, model, tt.visibleNames)
			}
		})
	}
}

func hoverAgents(first string, second string, third string) []session.AgentSession {
	return []session.AgentSession{
		{Harness: testSupportHarnessCodex, HarnessSession: first},
		{Harness: testSupportHarnessCodex, HarnessSession: second},
		{Harness: testSupportHarnessCodex, HarnessSession: third},
	}
}

func TestKeyboardNavigationAfterHoverRevealsSelectedAgentGroup(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyPressMsg
		want []string
	}{
		{
			name: "previous group",
			key:  tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl},
			want: []string{hoverEarlierProject, "earlier-first", "earlier-second", "earlier-third", hoverMiddleProject},
		},
		{
			name: "first child of hovered group",
			key:  keyCode(tea.KeyDown),
			want: []string{hoverMiddleProject, "middle-first", "middle-second", "middle-third", hoverLaterProject},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := crowdedHoverTestModel()
			layout := model.popupMouseLayout()
			model.View()
			model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+2))
			model.View()
			model = updateTestModel(t, model, tt.key)
			assertHoverVisibleRows(t, model, tt.want)
		})
	}
}

func TestAgentMetadataRefreshKeepsHoveredViewport(t *testing.T) {
	model := crowdedHoverTestModel()
	layout := model.popupMouseLayout()
	model.View()
	model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY))
	assertHoverVisibleRows(t, model, []string{"earlier-second", "earlier-third", hoverMiddleProject, "middle-first", "middle-second"})

	summaries := make([]session.AgentSummary, 0, len(model.sessions))
	for _, row := range model.sessions {
		summaries = append(summaries, session.AgentSummary{
			TmuxSessionName: row.Name,
			Total:           len(row.Agents),
			Agents:          row.Agents,
		})
	}

	summaries[0].Agents[1].Status = session.AgentStatusWaiting
	model = updateTestModel(t, model, agentSummariesMsg{summaries: summaries})

	assertHoverVisibleRows(t, model, []string{"earlier-second", "earlier-third", hoverMiddleProject, "middle-first", "middle-second"})

	if model.cursor != 0 || model.agentCursor != 1 || model.filtered[0].Agents[1].Status != session.AgentStatusWaiting {
		t.Fatalf("refreshed selection = %d/%d with status %q, want first session's second agent waiting", model.cursor, model.agentCursor, model.filtered[0].Agents[1].Status)
	}
}

func TestMouseWheelAfterHoverScrollsFromPresentedViewport(t *testing.T) {
	model := crowdedHoverTestModel()
	layout := model.popupMouseLayout()
	model.View()
	model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+2))
	assertHoverVisibleRows(t, model, []string{"earlier-second", "earlier-third", hoverMiddleProject, "middle-first", "middle-second"})

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))
	assertHoverVisibleRows(t, model, []string{hoverEarlierProject, "earlier-first", "earlier-second", "earlier-third", hoverMiddleProject})
}

func TestMouseWheelUpAfterHoveringFirstChildRevealsEarlierGroup(t *testing.T) {
	model := crowdedHoverTestModel()
	model.listTop = 4
	model.cursor = 1
	model.agentCursor = parentAgentIndex
	layout := model.popupMouseLayout()
	model.View()

	model = updateTestModel(t, model, mouseMotion(layout.bodyX, layout.listY+1))
	if model.cursor != 1 || model.agentCursor != 0 {
		t.Fatalf("hover selected %d/%d, want middle session's first child 1/0", model.cursor, model.agentCursor)
	}

	assertHoverVisibleRows(t, model, []string{hoverMiddleProject, "middle-first", "middle-second", "middle-third", hoverLaterProject})

	model = updateTestModel(t, model, mouseWheel(layout.bodyX, layout.listY, tea.MouseWheelUp))
	assertHoverVisibleRows(t, model, []string{hoverEarlierProject, "earlier-first", "earlier-second", "earlier-third", hoverMiddleProject})
}

func crowdedHoverTestModel() *Model {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 9

	model.sessions = []session.Session{
		{ID: "$1", Name: hoverEarlierProject, Agents: hoverAgents("earlier-first", "earlier-second", "earlier-third")},
		{ID: "$2", Name: hoverMiddleProject, Agents: hoverAgents("middle-first", "middle-second", "middle-third")},
		{ID: "$3", Name: hoverLaterProject, Agents: hoverAgents("later-first", "later-second", "later-third")},
	}
	for sessionIndex := range model.sessions {
		for agentIndex := range model.sessions[sessionIndex].Agents {
			agent := &model.sessions[sessionIndex].Agents[agentIndex]
			agent.RegistryID = agent.HarnessSession
		}
	}

	model.refreshDisplaySessions()
	model.filterSessions()
	model.listTop = 2
	model.agentCursor = 1

	return model
}

func assertHoverVisibleRows(t *testing.T, model *Model, want []string) {
	t.Helper()

	layout := model.popupMouseLayout()

	output := strings.Split(stripANSI(viewString(model.View())), "\n")
	for index, name := range want {
		if !strings.Contains(output[layout.listY+index], name) {
			t.Errorf("rendered row %d = %q, want %q", index, output[layout.listY+index], name)
		}
	}
}
