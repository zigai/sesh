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
			specs = agentColumnSpecs(left, false, nil)
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
			display.FieldAgentCI: {
				Text:     "failing",
				Role:     "error",
				Variants: nil,
				State:    rowlayout.Present,
			},
		},
	})

	var values sessionFormatValues

	left, _ := model.agentLayoutCells(
		sessionTableLayout{now: time.Unix(1, 0)},
		row,
		agent,
		true,
		&values,
	)

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

func TestNarrowAgentAndInboxRowsPrioritizeLeftAndSessionName(t *testing.T) {
	now := time.Now()
	row := session.Session{
		ID: "$1", Name: "work", AgentTotal: 1, AgentActive: 1, ActivePaneID: "%1",
		Agents: []session.AgentSession{{
			PaneID:           "%1",
			Harness:          "codex",
			HarnessSession:   "Fix narrow popup columns",
			CWD:              "/work/sesh",
			ProjectRoot:      "/work/sesh",
			Status:           session.AgentStatusRunning,
			StatusSince:      now.Add(-5 * time.Minute),
			Worktree:         "/work/sesh",
			GitBranch:        "feature/narrow-layout",
			GitChanges:       "+150 -22",
			GitAhead:         4,
			GitBehind:        1,
			GitTrackingKnown: true,
			Host:             "workstation",
		}},
	}

	for _, mode := range []displayMode{displayModeAgents, displayModeInbox} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTestModelWithConfig(t, config.DefaultConfig())
			model.displayMode = mode
			model.filtered = []session.Session{row}

			lines := strings.Split(stripANSI(model.renderList(44, 2)), "\n")
			agentLine := lines[1]

			if !strings.Contains(agentLine, "Fix narrow popup columns") {
				t.Fatalf("narrow %s row collapsed session name: %q", mode, agentLine)
			}

			if !strings.Contains(agentLine, "sesh") {
				t.Fatalf("narrow %s row lost left context: %q", mode, agentLine)
			}

			for _, hidden := range []string{"feature/narrow", "+150", "-22", GitGlyph} {
				if strings.Contains(agentLine, hidden) {
					t.Fatalf("narrow %s row kept right-side %q: %q", mode, hidden, agentLine)
				}
			}
		})
	}
}

func TestDisplayColumnPriorityOverridesDefaultNarrowOrder(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.ColumnPriority = []string{
		display.FieldTree,
		display.FieldStatusGlyph,
		display.FieldAgentGitLabel,
		display.FieldAgentGitAdded,
		display.FieldAgentGitDeleted,
	}
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		ID: "$1", Name: "work", AgentTotal: 1, AgentActive: 1,
		Agents: []session.AgentSession{{
			PaneID:           "%1",
			Harness:          "codex",
			HarnessSession:   "core-worker-long-name",
			CWD:              "/work/alpha",
			Status:           session.AgentStatusRunning,
			Worktree:         "/work/repo",
			GitBranch:        "feature/grid",
			GitChanges:       "+150 -2",
			GitAhead:         4,
			GitBehind:        1,
			GitTrackingKnown: true,
		}},
	}}

	lines := strings.Split(stripANSI(model.renderList(44, 2)), "\n")
	agentLine := lines[1]

	for _, kept := range []string{"feature/grid", "+150", "-2"} {
		if !strings.Contains(agentLine, kept) {
			t.Fatalf("custom priority row hid prioritized %q: %q", kept, agentLine)
		}
	}

	if strings.Contains(agentLine, "core-worker-long-name") {
		t.Fatalf("custom priority row kept full unprioritized session name: %q", agentLine)
	}
}

func TestNarrowSeshRowsPrioritizeSessionNameAndHonorColumnPriority(t *testing.T) {
	row := session.Session{
		ID:               "$1",
		Name:             "backend-api-server",
		Kind:             session.KindLive,
		ActiveWindowName: "neovim-editor",
		ActivePanePath:   "/work/backend-api-server",
		WindowCount:      4,
	}

	t.Run("default prioritizes session name over right columns", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Display.SeshFormat = "{session_name}{right}{active_window_name} {active_pane_path_name}"
		model := newTestModelWithConfig(t, cfg)
		model.filtered = []session.Session{row}

		line := stripANSI(model.renderList(32, 1))
		if !strings.Contains(line, "backend-api-server") {
			t.Fatalf("narrow sesh row collapsed session name: %q", line)
		}

		if strings.Contains(line, "neovim-editor") {
			t.Fatalf("narrow sesh row kept right column over session name: %q", line)
		}
	})

	t.Run("configured column priority keeps prioritized right column", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Display.SeshFormat = "{session_name}{right}{active_window_name} {active_pane_path_name}"
		cfg.Display.ColumnPriority = []string{
			display.FieldActiveWindowName,
			display.FieldSessionName,
		}
		model := newTestModelWithConfig(t, cfg)
		model.filtered = []session.Session{row}

		line := stripANSI(model.renderList(32, 1))
		if !strings.Contains(line, "neovim-editor") {
			t.Fatalf("custom priority sesh row hid prioritized right column: %q", line)
		}

		if !strings.Contains(line, "backend-api-ser…") {
			t.Fatalf(
				"custom priority sesh row did not shrink lower-priority session name: %q",
				line,
			)
		}
	})
}
