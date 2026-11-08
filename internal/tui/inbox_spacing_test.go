package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestInboxSectionsHaveOneBlankRowBetweenHeadings(t *testing.T) {
	model := newInboxTestModel(t, nil, nil)
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})
	model.config.Inbox.Collapsed = []string{config.InboxClosed, config.InboxArchived}
	model.filtered = []session.Session{
		inboxGroupRow(config.InboxReady, "Ready", []session.AgentSession{
			inboxTestAgent("ready-agent", "%1", session.AgentStatusIdle, time.Now()),
		}),
		inboxGroupRow(config.InboxWorking, "Working", []session.AgentSession{
			inboxTestAgent("working-agent", "%2", session.AgentStatusRunning, time.Now()),
		}),
		inboxGroupRow(config.InboxClosed, "Closed (2)", nil),
		inboxGroupRow(config.InboxArchived, "Archived (1)", nil),
	}
	model.cursor, model.agentCursor = 0, 0

	rows := model.popupListRows()
	if len(rows) != 9 || rows[0].isSeparator() || rows[len(rows)-1].isSeparator() {
		t.Fatalf("unexpected section rows: %#v", rows)
	}

	for _, index := range []int{2, 5, 7} {
		row := rows[index]
		if !row.isSeparator() || model.rowSelectable(row) {
			t.Fatalf("row %d must be a non-selectable spacer: %#v", index, row)
		}
	}

	lines := strings.Split(stripANSI(model.View().Content), "\n")

	for _, heading := range []string{"Working", "Closed (2)", "Archived (1)"} {
		found := false

		for i, line := range lines {
			if strings.Trim(line, " │") != heading {
				continue
			}

			found = true

			if i < 2 || strings.Trim(lines[i-1], " │") != "" ||
				strings.Trim(lines[i-2], " │") == "" {
				t.Fatalf(
					"heading %q must follow exactly one blank line:\n%s",
					heading,
					strings.Join(lines, "\n"),
				)
			}
		}

		if !found {
			t.Fatalf("heading %q missing from view:\n%s", heading, strings.Join(lines, "\n"))
		}
	}

	layout := model.popupMouseLayout()

	model = updateTestModel(t, model, mouseClick(layout.bodyX+5, layout.listY+2, tea.MouseLeft))
	if model.cursor != 0 || model.agentCursor != 0 {
		t.Fatal("clicking a section spacer changed selection")
	}

	model = updateTestModel(t, model, keyCode(tea.KeyDown))
	if model.cursor != 1 || model.agentCursor != 0 {
		t.Fatalf(
			"navigation did not skip section spacer: cursor=%d agent=%d",
			model.cursor,
			model.agentCursor,
		)
	}
}
