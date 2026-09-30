package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestMouseHoverKeepsScrolledSessionsInPlace(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		rows  []session.Session
		steps int
	}{
		{name: "ordinary sessions", rows: nil, steps: 11},
		{
			name: "ordinary sessions after agents",
			rows: []session.Session{{
				Name: "agent-project",
				Agents: []session.AgentSession{{
					Harness: testSupportHarnessCodex, HarnessSession: "agent-session",
				}},
			}},
			steps: 13,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model := newTestModel()
			model.mode = modePopup
			model.displayMode = displayModeAgents
			model.agentCursor = parentAgentIndex
			model.width = 80
			model.height = 9
			model.filtered = testCase.rows

			for index := range 12 {
				model.filtered = append(model.filtered, session.Session{Name: fmt.Sprintf("session-%02d", index)})
			}

			model.View()

			for range testCase.steps {
				model = updateTestModel(t, model, keyCode(tea.KeyDown))
				model.View()
			}

			assertScrolledSessionRows(t, model)

			for motion := range 5 {
				model = updateTestModel(t, model, mouseMotion(2+motion, 4))
				assertScrolledSessionRows(t, model)

				lines := strings.Split(stripANSI(viewString(model.View())), "\n")
				if !strings.HasPrefix(lines[4], "│ ▌ ") {
					t.Fatalf("hovered row = %q, want highlighted session-09", lines[4])
				}
			}
		})
	}
}

func assertScrolledSessionRows(t *testing.T, model *Model) {
	t.Helper()

	lines := strings.Split(stripANSI(viewString(model.View())), "\n")
	wantNames := []string{"session-07", "session-08", "session-09", "session-10", "session-11"}

	for index, name := range wantNames {
		if !strings.Contains(lines[index+2], name) {
			t.Fatalf("row %d = %q, want %s in its original position", index, lines[index+2], name)
		}
	}
}
