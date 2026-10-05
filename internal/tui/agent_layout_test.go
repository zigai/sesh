package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	rowlayout "github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

func TestAgentRowsUseCompactStatusAcrossWholeColumn(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.AgentsAgentFormat = "{status_text}"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents
	row := session.Session{Agents: []session.AgentSession{
		{PaneID: "%1", Status: session.AgentStatusRunning},
		{PaneID: "%2", Status: session.AgentStatusIdle},
	}}
	table := sessionTableLayout{now: time.Unix(1, 0)}
	rows := make([][]rowlayout.Cell, 0, len(row.Agents))

	var specs []rowlayout.ColumnSpec

	var values sessionFormatValues

	for _, agent := range row.Agents {
		left, _ := model.agentLayoutCells(table, row, agent, false, &values)
		if specs == nil {
			specs = agentColumnSpecs(left)
		}

		rows = append(rows, agentCells(values, left))
	}

	plan := rowlayout.Plan(specs, rowlayout.Measure(specs, rows), 1, nil, table.now)
	if !plan.Columns[0].Compact || !plan.Columns[0].Visible {
		t.Fatalf("status column did not use its compact variant: %+v", plan.Columns[0])
	}

	for i, cells := range rows {
		painted, spans := paintRow(plan, cells)

		want := displayedAgentStatusGlyphChar(row.Agents[i].Status, false)
		if ansi.Strip(painted) != want || spans[0].Offset != 0 {
			t.Fatalf("agent %d compact row = %q, spans = %+v, want %q", i, painted, spans, want)
		}
	}
}

func TestAgentRowsPreserveProviderStyleRole(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.AgentsAgentFormat = "{agent_ci}"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents
	agent := session.AgentSession{PaneID: "%1"}
	row := session.Session{Agents: []session.AgentSession{agent}}
	model.providerRunner.Publish(providers.GH, providers.Rows{
		agent.PaneID: {
			display.FieldAgentCI: {Text: "failing", Role: "error", Variants: nil, State: rowlayout.Present},
		},
	})

	var values sessionFormatValues

	left, _ := model.agentLayoutCells(sessionTableLayout{now: time.Unix(1, 0)}, row, agent, true, &values)

	cells := agentCells(values, left)
	if len(cells) != 1 || cells[0].Text != "failing" || cells[0].Role != "error" {
		t.Fatalf("provider cell lost its role: %+v", cells)
	}
}

func TestSpinnerTracksAgentStatusAcrossStatusStyles(t *testing.T) {
	for _, style := range []string{"glyph", "text"} {
		t.Run(style, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.AgentSessions.StatusStyle = style
			cfg.Display.AgentsAgentFormat = "{spinner}"
			model := newTestModelWithConfig(t, cfg)
			model.displayMode = displayModeAgents
			agent := session.AgentSession{PaneID: "%1", Status: session.AgentStatusRunning}
			row := session.Session{Agents: []session.AgentSession{agent}}
			table := sessionTableLayout{now: time.Unix(1, 0)}

			var values sessionFormatValues

			left, _ := model.agentLayoutCells(table, row, agent, true, &values)
			if len(left) != 1 || left[0].Value == "" || left[0].State != rowlayout.Present {
				t.Fatalf("running spinner = %+v", left)
			}

			agent.Status = session.AgentStatusIdle

			left, _ = model.agentLayoutCells(table, row, agent, true, &values)
			if len(left) != 1 || left[0].Value != "" || left[0].State != rowlayout.Absent {
				t.Fatalf("idle spinner = %+v", left)
			}
		})
	}
}

func paintRow(plan rowlayout.ColumnPlan, cells []rowlayout.Cell) (string, []rowlayout.Span) {
	parts, spans := rowlayout.PaintColumns(plan, cells, nil)

	return strings.Join(parts, ""), spans
}
