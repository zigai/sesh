package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestRenderAgentsModeShowsOneIndentedLinePerAgent(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:        testSupportSessionProjectName,
		AgentTotal:  2,
		AgentActive: 1,
		Agents: []session.AgentSession{
			{Harness: testSupportHarnessCodex, HarnessSession: "codex-session", Status: session.AgentStatusRunning, StatusSince: now.Add(-time.Minute)},
			{Harness: "claude", HarnessSession: "claude-session", PaneID: "%8", Status: session.AgentStatusIdle, StatusSince: now.Add(-3 * time.Minute)},
		},
	}}
	model.state.RecordAgentView(model.filtered[0].SemanticIdentity(), "%8")

	output := stripANSI(model.renderList(80, 3))
	lines := strings.Split(output, "\n")

	t.Run("glyph style", func(t *testing.T) {
		if len(lines) != 3 {
			t.Fatalf("agent hierarchy lines = %d, want 3: %q", len(lines), output)
		}

		requireTextContainsAll(t, output, testSupportSessionProjectName, "├─ Codex  · codex-session", "●", "└─ Claude · claude-session", "○")

		runningPrefix, _, runningFound := strings.Cut(lines[1], "●")

		idlePrefix, _, idleFound := strings.Cut(lines[2], "○")
		if !runningFound || !idleFound {
			t.Fatalf("child statuses are missing: %q / %q", lines[1], lines[2])
		}

		if runningColumn, idleColumn := lipgloss.Width(runningPrefix), lipgloss.Width(idlePrefix); runningColumn != idleColumn {
			t.Fatalf("child statuses are not aligned: %q / %q", lines[1], lines[2])
		}

		if strings.Contains(lines[0], "WORKING") || strings.Contains(lines[0], "IDLE") {
			t.Fatalf("expanded parent repeats child status: %q", lines[0])
		}

		if strings.Contains(lines[0], "1/2") {
			t.Fatalf("AGENTS heading unexpectedly shows aggregate count: %q", lines[0])
		}

		for index, line := range lines {
			if width := lipgloss.Width(line); width != 80 {
				t.Fatalf("line %d width = %d, want 80: %q", index, width, line)
			}
		}
	})

	t.Run("text style", func(t *testing.T) {
		model.config.AgentSessions.StatusStyle = "text"
		textOutput := stripANSI(model.renderList(80, 3))
		requireTextContainsAll(t, textOutput, testSupportSessionProjectName, "├─ Codex  · codex-session", "WORKING", "└─ Claude · claude-session", "IDLE")
	})
}

func TestDefaultAgentFormatMaintainsStatusAlignmentAcrossAges(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:         testSupportSessionProjectName,
		ActivePaneID: "%1",
		IsCurrent:    true,
		AgentTotal:   4,
		AgentActive:  1,
		Agents: []session.AgentSession{
			{Harness: testSupportHarnessCodex, HarnessSession: "s1", PaneID: "%1", Status: session.AgentStatusRunning, StatusSince: now.Add(-(time.Hour + 2*time.Minute))},
			{Harness: "claude", HarnessSession: "s2", PaneID: "%2", Status: session.AgentStatusIdle, StatusSince: now.Add(-20 * time.Minute)},
			{Harness: "pi", HarnessSession: "s3", PaneID: "%3", Status: session.AgentStatusIdle, StatusSince: now.Add(-8 * time.Minute)},
			{Harness: "hermes", HarnessSession: "s4", PaneID: "%4", Status: session.AgentStatusRunning, StatusSince: now.Add(-15 * time.Minute)},
		},
	}}

	output := stripANSI(model.renderList(80, 5))

	lines := strings.Split(output, "\n")
	if len(lines) != 5 {
		t.Fatalf("rendered lines count = %d, want 5: %q", len(lines), output)
	}

	var glyphCols []int

	for i := 1; i <= 4; i++ {
		line := lines[i]

		glyphIdx := strings.IndexAny(line, "●○◎")
		if glyphIdx < 0 {
			t.Fatalf("line %d has no status glyph: %q", i, line)
		}

		glyphCols = append(glyphCols, lipgloss.Width(line[:glyphIdx]))
	}

	for i := 1; i < len(glyphCols); i++ {
		if glyphCols[i] != glyphCols[0] {
			t.Fatalf("status glyph at row %d column %d does not match row 1 column %d:\n%s", i+1, glyphCols[i], glyphCols[0], output)
		}
	}
}

func TestAgentFormatSupportsClockStatusTimeFormat(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.config.AgentSessions.StatusTimeFormat = "clock"
	model.filtered = []session.Session{{
		Name:         testSupportSessionProjectName,
		ActivePaneID: "%1",
		IsCurrent:    true,
		AgentTotal:   3,
		AgentActive:  1,
		Agents: []session.AgentSession{
			{Harness: testSupportHarnessCodex, HarnessSession: "s1", PaneID: "%1", Status: session.AgentStatusRunning, StatusSince: now.Add(-(time.Hour + 2*time.Minute))},
			{Harness: "claude", HarnessSession: "s2", PaneID: "%2", Status: session.AgentStatusIdle, StatusSince: now.Add(-(time.Minute + 23*time.Second))},
			{Harness: "pi", HarnessSession: "s3", PaneID: "%3", Status: session.AgentStatusIdle, StatusSince: now.Add(-45 * time.Second)},
		},
	}}

	t.Run("glyph style with clock", func(t *testing.T) {
		output := stripANSI(model.renderList(80, 4))
		requireTextContainsAll(t, output, "01:02:00", "00:01:23", "00:00:45")

		lines := strings.Split(output, "\n")
		if len(lines) != 4 {
			t.Fatalf("rendered lines count = %d, want 4: %q", len(lines), output)
		}

		var glyphCols []int

		for i := 1; i <= 3; i++ {
			line := lines[i]

			glyphIdx := strings.IndexAny(line, "●○◎")
			if glyphIdx < 0 {
				t.Fatalf("line %d has no status glyph: %q", i, line)
			}

			glyphCols = append(glyphCols, lipgloss.Width(line[:glyphIdx]))
		}

		for i := 1; i < len(glyphCols); i++ {
			if glyphCols[i] != glyphCols[0] {
				t.Fatalf("status glyph at row %d column %d does not match row 1 column %d:\n%s", i+1, glyphCols[i], glyphCols[0], output)
			}
		}
	})

	t.Run("text style with clock", func(t *testing.T) {
		model.config.AgentSessions.StatusStyle = "text"
		output := stripANSI(model.renderList(80, 4))
		requireTextContainsAll(t, output, "01:02:00", "WORKING", "00:01:23", "IDLE", "00:00:45")
	})

	t.Run("status_time_style alias with 00:01:23 format value", func(t *testing.T) {
		model.config.AgentSessions.StatusTimeFormat = ""
		model.config.AgentSessions.StatusTimeStyle = "00:01:23"
		model.config.AgentSessions.StatusStyle = "glyph"
		output := stripANSI(model.renderList(80, 4))
		requireTextContainsAll(t, output, "01:02:00", "00:01:23", "00:00:45")
	})
}

func TestUnreadIdleAgentUsesDistinctStatusUntilViewed(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:       testSupportSessionProjectName,
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness:        testSupportHarnessCodex,
			HarnessSession: "finished-task",
			PaneID:         "%7",
			Status:         session.AgentStatusIdle,
			StatusSince:    now.Add(-time.Minute),
		}},
	}}

	unreadGlyph := stripANSI(model.renderList(60, 2))
	if !strings.Contains(unreadGlyph, "◎") ||
		strings.Contains(unreadGlyph, "○") ||
		strings.Contains(unreadGlyph, "●") {
		t.Fatalf("unread idle row = %q, want emphasized idle ring distinct from working", unreadGlyph)
	}

	model.config.AgentSessions.StatusStyle = "text"

	unreadText := stripANSI(model.renderList(60, 2))
	if !strings.Contains(unreadText, "IDLE*") {
		t.Fatalf("unread idle text row = %q, want IDLE*", unreadText)
	}

	model.state.RecordAgentView(model.filtered[0].SemanticIdentity(), "%7")
	model.config.AgentSessions.StatusStyle = "glyph"

	viewedGlyph := stripANSI(model.renderList(60, 2))
	if !strings.Contains(viewedGlyph, "○") || strings.Contains(viewedGlyph, "◎") {
		t.Fatalf("viewed idle row = %q, want regular idle glyph", viewedGlyph)
	}

	model.config.AgentSessions.StatusStyle = "text"

	viewedText := stripANSI(model.renderList(60, 2))
	if !strings.Contains(viewedText, "IDLE") || strings.Contains(viewedText, "IDLE*") {
		t.Fatalf("viewed idle text row = %q, want regular IDLE", viewedText)
	}
}

func TestCurrentActiveIdleAgentIsAlreadyViewed(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:         testSupportSessionProjectName,
		IsCurrent:    true,
		ActivePaneID: "%7",
		AgentTotal:   1,
		Agents: []session.AgentSession{{
			PaneID:      "%7",
			Status:      session.AgentStatusIdle,
			StatusSince: now.Add(-time.Minute),
		}},
	}}

	output := stripANSI(model.renderList(60, 2))
	if !strings.Contains(output, "○") || strings.Contains(output, "◎") {
		t.Fatalf("current active idle row = %q, want viewed idle status", output)
	}
}

func TestCurrentActiveIdleAgentRecordsViewAndSurvivesSessionChange(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model = updateTestModel(t, model, sessionLoadResultMsg{sessions: []session.Session{{
		ID:           "$1",
		Name:         testSupportSessionProjectName,
		IsCurrent:    true,
		ActivePaneID: "%7",
		AgentTotal:   1,
		Agents: []session.AgentSession{{
			PaneID:      "%7",
			Status:      session.AgentStatusIdle,
			StatusSince: now.Add(-time.Minute),
		}},
	}}})

	output := stripANSI(model.renderList(60, 2))
	if !strings.Contains(output, "○") || strings.Contains(output, "◎") {
		t.Fatalf("current active idle row = %q, want viewed idle status", output)
	}

	persistAgentViewsForTest(t, model)

	if got := model.state.GetLastAgentView(model.filtered[0].SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("current active idle agent did not record viewed state in history")
	}

	model.filtered[0].IsCurrent = false
	model.filtered[0].ActivePaneID = "%99"

	outputAfterSwitch := stripANSI(model.renderList(60, 2))
	if !strings.Contains(outputAfterSwitch, "○") || strings.Contains(outputAfterSwitch, "◎") {
		t.Fatalf("idle row after leaving pane = %q, want viewed status to persist", outputAfterSwitch)
	}
}

func TestActiveIdleAgentAutoRecordsViewOnLoadAndSurvivesSessionChange(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model = updateTestModel(t, model, sessionLoadResultMsg{sessions: []session.Session{{
		ID:           "$1",
		Name:         testSupportSessionProjectName,
		IsCurrent:    true,
		ActivePaneID: "%7",
		AgentTotal:   1,
		Agents: []session.AgentSession{{
			PaneID:      "%7",
			Status:      session.AgentStatusIdle,
			StatusSince: now.Add(-time.Minute),
		}},
	}}})

	if got := model.state.GetLastAgentView(model.filtered[0].SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("current active idle agent view was not automatically recorded on session load")
	}

	model.filtered[0].IsCurrent = false
	model.filtered[0].ActivePaneID = "%99"

	outputAfterSwitch := stripANSI(model.renderList(60, 2))
	if !strings.Contains(outputAfterSwitch, "○") || strings.Contains(outputAfterSwitch, "◎") {
		t.Fatalf("idle row after leaving pane = %q, want viewed status to persist", outputAfterSwitch)
	}
}

func TestMultiAgentSessionSwitchingPaneKeepsViewedAgentVisited(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model = updateTestModel(t, model, sessionLoadResultMsg{sessions: []session.Session{{
		ID:           "$1",
		Name:         testSupportSessionProjectName,
		IsCurrent:    true,
		ActivePaneID: "%7",
		AgentTotal:   2,
		Agents: []session.AgentSession{
			{
				Harness:        testSupportHarnessCodex,
				HarnessSession: "agent-1",
				PaneID:         "%7",
				Status:         session.AgentStatusIdle,
				StatusSince:    now.Add(-time.Minute),
			},
			{
				Harness:        "claude",
				HarnessSession: "agent-2",
				PaneID:         "%8",
				Status:         session.AgentStatusIdle,
				StatusSince:    now.Add(-time.Minute),
			},
		},
	}}})

	if got := model.state.GetLastAgentView(model.filtered[0].SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("agent 1 view was not recorded")
	}

	model.filtered[0].ActivePaneID = "%8"

	output := stripANSI(model.renderList(80, 3))
	lines := strings.Split(output, "\n")

	if len(lines) < 3 {
		t.Fatalf("rendered lines = %d, want at least 3: %q", len(lines), output)
	}

	if !strings.Contains(lines[1], "○") || strings.Contains(lines[1], "◎") {
		t.Fatalf("agent 1 row = %q, want visited status to persist when switching to another pane", lines[1])
	}
}

func TestIdleAgentWithSessionActivityAfterIdleIsViewed(t *testing.T) {
	now := time.Now()
	model := newTestModel()
	model.displayMode = displayModeAgents
	model = updateTestModel(t, model, sessionLoadResultMsg{sessions: []session.Session{{
		ID:           "$1",
		Name:         testSupportSessionProjectName,
		IsCurrent:    false,
		ActivePaneID: "%7",
		Activity:     now.Add(-30 * time.Second),
		AgentTotal:   1,
		Agents: []session.AgentSession{{
			PaneID:      "%7",
			Status:      session.AgentStatusIdle,
			StatusSince: now.Add(-time.Minute),
		}},
	}}})

	output := stripANSI(model.renderList(60, 2))
	if !strings.Contains(output, "○") || strings.Contains(output, "◎") {
		t.Fatalf("idle row with session activity after idle = %q, want viewed status", output)
	}

	persistAgentViewsForTest(t, model)

	if got := model.state.GetLastAgentView(model.filtered[0].SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("idle agent with session activity did not record viewed state")
	}
}

func TestIdleAgentWithSessionSwitchAfterIdleIsViewed(t *testing.T) {
	now := time.Now()
	row := session.Session{
		ID:           "$1",
		Name:         testSupportSessionProjectName,
		IsCurrent:    false,
		ActivePaneID: "%7",
		AgentTotal:   1,
		Agents: []session.AgentSession{{
			PaneID:      "%7",
			Status:      session.AgentStatusIdle,
			StatusSince: now.Add(-time.Minute),
		}},
	}

	state := testState()
	state.SwitchHistory = append(state.SwitchHistory, session.SwitchHistoryEntry{
		Session:    row.SemanticIdentity(),
		LastSwitch: now.Add(-20 * time.Second).Unix(),
	})

	model, err := newModelWithDeps(config.DefaultConfig(), state, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(model.Close)

	model.displayMode = displayModeAgents
	model = updateTestModel(t, model, sessionLoadResultMsg{sessions: []session.Session{row}})

	output := stripANSI(model.renderList(60, 2))
	if !strings.Contains(output, "○") || strings.Contains(output, "◎") {
		t.Fatalf("idle row with session switch after idle = %q, want viewed status", output)
	}

	persistAgentViewsForTest(t, model)

	if got := model.state.GetLastAgentView(model.filtered[0].SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("idle agent with session switch did not record viewed state")
	}
}

func persistAgentViewsForTest(t *testing.T, model *Model) {
	t.Helper()

	command := model.persistAgentViewsCommand(model.recordActiveAgentViews())
	if command == nil {
		t.Fatal("no agent view was scheduled for persistence")
	}

	updateTestModel(t, model, command())
}

func requireTextContainsAll(t *testing.T, text string, values ...string) {
	t.Helper()

	for _, value := range values {
		if !strings.Contains(text, value) {
			t.Fatalf("text = %q, want %q", text, value)
		}
	}
}

func TestRenderSeshModeDoesNotExpandAgentChildren(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{
		Name:       testSupportSessionProjectName,
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness: testSupportHarnessCodex, HarnessSession: "codex-session", Status: session.AgentStatusRunning,
		}},
	}}

	output := stripANSI(model.renderList(60, 3))
	if strings.Contains(output, "Codex") || strings.Contains(output, "codex-session") {
		t.Fatalf("SESH output expanded agent details: %q", output)
	}
}

func TestRenderAgentInActiveTmuxPaneShowsMarkerBeforeStatus(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.agentCursor = 1
	model.filtered = []session.Session{{
		Name:         testSupportSessionProjectName,
		ActivePaneID: "%7",
		AgentTotal:   2,
		Agents: []session.AgentSession{
			{Harness: "pi", HarnessSession: "active-agent", PaneID: "%7", Status: session.AgentStatusIdle},
			{Harness: testSupportHarnessCodex, HarnessSession: "other-agent", PaneID: "%8", Status: session.AgentStatusIdle},
		},
	}}

	output := model.renderList(60, 3)

	lines := strings.Split(stripANSI(output), "\n")
	markerIndex := strings.Index(lines[1], "*")

	statusIndex := strings.Index(lines[1], "○")
	if !strings.Contains(lines[1], "Pi     · active-agent") || markerIndex < 0 || statusIndex < 0 || markerIndex >= statusIndex {
		t.Fatalf("active agent row = %q, want active-pane marker before glyph", lines[1])
	}

	if strings.Contains(lines[2], "*") {
		t.Fatalf("inactive agent row unexpectedly marked: %q", lines[2])
	}

	activeSessionPrefix, _, activeFound := strings.Cut(lines[1], "active-agent")

	inactiveSessionPrefix, _, inactiveFound := strings.Cut(lines[2], "other-agent")
	if !activeFound || !inactiveFound || lipgloss.Width(activeSessionPrefix) != lipgloss.Width(inactiveSessionPrefix) {
		t.Fatalf("agent session columns = %d/%d, want aligned rows: %q", lipgloss.Width(activeSessionPrefix), lipgloss.Width(inactiveSessionPrefix), lines)
	}
}

func TestRenderSelectedAgentChildUsesSelectionBar(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.agentCursor = 0
	model.filtered = []session.Session{{
		Name:       testSupportSessionProjectName,
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness: "pi", HarnessSession: "agent-id", PaneID: "%7", Status: session.AgentStatusRunning,
		}},
	}}

	lines := strings.Split(stripANSI(model.renderList(60, 2)), "\n")
	if strings.HasPrefix(lines[0], "▌") || !strings.HasPrefix(lines[1], "▌") {
		t.Fatalf("selection bar is not on child row: %q", lines)
	}
}

func TestRenderAgentChildrenStaysBoundedAtNarrowWidth(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.agentSummaryErr = errSaveFailed
	model.filtered = []session.Session{{
		Name:         "project-with-a-long-name",
		ActivePaneID: "%3",
		AgentTotal:   1,
		AgentUnknown: 1,
		Agents: []session.AgentSession{{
			Harness:        "claude",
			HarnessSession: "a-very-long-harness-session-identity",
			PaneID:         "%3",
			Status:         session.AgentStatusUnknown,
		}},
	}}

	output := stripANSI(model.renderList(48, 2))

	lines := strings.Split(output, "\n")
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 48 {
			t.Fatalf("line %d width = %d, want 48: %q", index, width, line)
		}
	}

	markerIndex := strings.Index(lines[1], "*")

	statusIndex := strings.Index(lines[1], "○")
	if !strings.Contains(lines[1], "Claude") || markerIndex < 0 || statusIndex < 0 || markerIndex >= statusIndex {
		t.Fatalf("narrow child row lost required context: %q", lines[1])
	}
}

func TestRenderAgentChildrenUsesConfiguredMutedTheme(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.config.Theme.Muted = "#123456"
	model.agentCursor = 1
	model.filtered = []session.Session{{
		Name:       testSupportSessionProjectName,
		AgentTotal: 2,
		Agents: []session.AgentSession{
			{Harness: testSupportHarnessCodex, HarnessSession: testSupportAgentRegistryID, Status: session.AgentStatusRunning},
			{Harness: "pi", HarnessSession: "agent-2", Status: session.AgentStatusIdle},
		},
	}}

	output := model.renderList(60, 2)
	styledProbe := lipgloss.NewStyle().Foreground(lipgloss.Color("#123456")).Render("x")

	colorPrefix, _, ok := strings.Cut(styledProbe, "x")
	if !ok || colorPrefix == "" || !strings.Contains(output, colorPrefix) {
		t.Fatalf("agent child does not use configured muted theme: %q", output)
	}
}

func TestAgentVisualRowsMakeChildrenSelectable(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{
		{Name: "first", Agents: []session.AgentSession{{Harness: "pi"}, {Harness: testSupportHarnessCodex}}},
		{Name: "second", Agents: []session.AgentSession{{Harness: "claude"}}},
	}

	rows := model.popupListRows()
	if len(rows) != 5 {
		t.Fatalf("visual rows = %#v, want two parents and three children", rows)
	}

	wantShortcuts := []popupListRow{
		{sessionIndex: 0, agentIndex: 0},
		{sessionIndex: 0, agentIndex: 1},
	}
	if got := model.visibleShortcutRows(); !slices.Equal(got, wantShortcuts) {
		t.Fatalf("visible shortcut rows = %#v, want %#v", got, wantShortcuts)
	}

	model.cursor = 1

	wantSession1Shortcuts := []popupListRow{
		{sessionIndex: 1, agentIndex: 0},
	}
	if got := model.visibleShortcutRows(); !slices.Equal(got, wantSession1Shortcuts) {
		t.Fatalf("visible shortcut rows for session 1 = %#v, want %#v", got, wantSession1Shortcuts)
	}

	model.cursor = 0

	layout := model.popupMouseLayout()
	childMouse := mouseClick(layout.bodyX, layout.listY+1, 0)

	row, ok := model.listRowAtMouse(childMouse.Mouse())
	if !ok || row.sessionIndex != 0 || row.agentIndex != 0 {
		t.Fatalf("child mouse maps to %#v/%v, want first child", row, ok)
	}
}

func TestAgentChildEnterDefersExactPaneSwitch(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.agentCursor = 0
	model.filtered = []session.Session{{
		Name: testSupportSessionProjectName,
		Agents: []session.AgentSession{{
			Harness: "pi", HarnessSession: "agent-id", PaneID: "%7",
		}},
	}}

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil for selected agent")
	}

	_, cmd := handler()
	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("agent command message = %T, want sessionActionResultMsg", rawMsg)
	}

	if msg.operation != tuiOperationActivateAgentPane || !msg.deferred {
		t.Fatalf("agent command metadata = %#v, want typed deferred activation", msg)
	}

	updated, quit := model.handleSessionActionResult(msg)

	selected, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model = %T, want *Model", updated)
	}

	if quit == nil || selected.DeferredSwitchSession() != testSupportSessionProjectName || selected.DeferredSwitchPane() != "%7" {
		t.Fatalf(
			"deferred agent target = %q/%q with quit %v, want project/%%7/true",
			selected.DeferredSwitchSession(),
			selected.DeferredSwitchPane(),
			quit != nil,
		)
	}
}

func TestAgentChildMouseClickDefersExactPaneSwitch(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{{
		Name: testSupportSessionProjectName,
		Agents: []session.AgentSession{{
			Harness: testSupportHarnessCodex, HarnessSession: "agent-id", PaneID: "%9",
		}},
	}}

	layout := model.popupMouseLayout()

	handler := model.handleMouseClick(mouseClick(layout.bodyX, layout.listY+1, tea.MouseLeft).Mouse())
	if handler == nil {
		t.Fatal("child mouse click returned nil handler")
	}

	_, cmd := handler()

	msg, ok := cmd().(sessionActionResultMsg)
	if !ok ||
		msg.switchSession != testSupportSessionProjectName ||
		msg.switchPane != "%9" ||
		msg.operation != tuiOperationActivateAgentPane ||
		!msg.deferred {
		t.Fatalf("child mouse action = %#v/%v, want typed deferred project pane %%9", msg, ok)
	}
}

func TestAgentHeadingMouseClickSelectsAndEntersSession(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.agentCursor = 1
	model.filtered = []session.Session{{
		Name: testSupportSessionProjectName,
		Agents: []session.AgentSession{
			{Harness: "pi", HarnessSession: "first", PaneID: "%7"},
			{Harness: testSupportHarnessCodex, HarnessSession: "selected", PaneID: "%8"},
		},
	}}

	layout := model.popupMouseLayout()

	handler := model.handleMouseClick(mouseClick(layout.bodyX, layout.listY, tea.MouseLeft).Mouse())
	if handler == nil {
		t.Fatal("heading click was not consumed")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("heading click returned nil command")
	}

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex}); !got.equal(want) {
		t.Fatalf("selection after heading click = %#v, want heading %#v", got, want)
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok || msg.switchSession != testSupportSessionProjectName || msg.switchPane != "" || !msg.deferred {
		t.Fatalf("heading mouse action = %#v/%v, want deferred session switch without pane", msg, ok)
	}
}

func TestAgentHeadingMouseMotionSelectsHeading(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.agentCursor = 1
	model.filtered = []session.Session{{
		Name:   testSupportSessionProjectName,
		Agents: []session.AgentSession{{HarnessSession: "first"}, {HarnessSession: "selected"}},
	}}

	layout := model.popupMouseLayout()
	if handler := model.handleMouseMotion(mouseClick(layout.bodyX, layout.listY, 0).Mouse()); handler == nil {
		t.Fatal("heading mouse motion returned nil handler")
	}

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 0, agentIndex: parentAgentIndex}); !got.equal(want) {
		t.Fatalf("selection after heading hover = %#v, want heading %#v", got, want)
	}
}

func TestPageNavigationMovesOnlyAmongAgentRows(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 12
	model.filtered = []session.Session{
		{Name: "first", Agents: threeAgentsForTest()},
		{Name: "second", Agents: threeAgentsForTest()},
		{Name: "third", Agents: threeAgentsForTest()},
	}
	model.agentCursor = 1
	before := model.selectedSelectableIndex(model.popupSelectableRows())

	model.moveSelectionByPage(1)

	afterDown := model.selectedSelectableIndex(model.popupSelectableRows())
	if !model.selectedListRow().isAgent() || afterDown <= before {
		t.Fatalf("page-down selection/index = %#v/%d, want a later agent than %d", model.selectedListRow(), afterDown, before)
	}

	model.moveSelectionByPage(-1)

	if !model.selectedListRow().isAgent() || model.selectedSelectableIndex(model.popupSelectableRows()) >= afterDown {
		t.Fatalf("page-up selection = %#v, want an earlier agent", model.selectedListRow())
	}
}

func TestAgentMouseScrollCanSelectChildRow(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 8
	model.filtered = []session.Session{
		{Name: "first", Agents: []session.AgentSession{{Harness: "pi"}, {Harness: testSupportHarnessCodex}}},
		{Name: "second", Agents: []session.AgentSession{{Harness: "claude"}}},
		{Name: "third", Agents: []session.AgentSession{{Harness: "grok"}}},
	}

	if !model.scrollListBy(3) {
		t.Fatal("scrollListBy() = false, want visual scroll")
	}

	if model.cursor != 1 || model.agentCursor != parentAgentIndex {
		t.Fatalf("scroll selection = %d/%d, want second session heading", model.cursor, model.agentCursor)
	}
}

func TestAgentHarnessColumnAlignmentVariableLengths(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:       "multi-harness",
		AgentTotal: 4,
		Agents: []session.AgentSession{
			{Harness: "pi", HarnessSession: "pi-session", Status: session.AgentStatusRunning},
			{Harness: "omp", HarnessSession: "omp-session", Status: session.AgentStatusWaiting},
			{Harness: testSupportHarnessCodex, HarnessSession: "codex-session", Status: session.AgentStatusIdle},
			{Harness: "claude", HarnessSession: "claude-session", Status: session.AgentStatusUnknown},
		},
	}}

	output := stripANSI(model.renderList(80, 5))

	lines := strings.Split(output, "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5: %q", len(lines), output)
	}

	var (
		dotColumns     []int
		sessionColumns []int
	)

	for i := 1; i <= 4; i++ {
		prefix, rest, found := strings.Cut(lines[i], " · ")
		if !found {
			t.Fatalf("line %d missing dot separator: %q", i, lines[i])
		}

		dotColumns = append(dotColumns, lipgloss.Width(prefix))
		sessionColumns = append(sessionColumns, lipgloss.Width(prefix)+lipgloss.Width(" · "))

		if !strings.HasPrefix(rest, []string{"pi-session", "omp-session", "codex-session", "claude-session"}[i-1]) {
			t.Fatalf("line %d unexpected session name in %q", i, rest)
		}
	}

	for i := 1; i < len(dotColumns); i++ {
		if dotColumns[i] != dotColumns[0] {
			t.Fatalf("dot separators not aligned: got %v across lines: %q", dotColumns, lines)
		}

		if sessionColumns[i] != sessionColumns[0] {
			t.Fatalf("session names not aligned: got %v across lines: %q", sessionColumns, lines)
		}
	}
}

func TestAgentMouseScrollCanTraverseOversizedSelectedGroup(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 10
	model.filtered = []session.Session{
		{Name: "large", Agents: make([]session.AgentSession, 9)},
		{Name: "second"},
	}

	if !model.scrollListBy(3) {
		t.Fatal("scrollListBy() = false, want visual scroll")
	}

	rows := model.popupListRows()

	height := model.popupMouseLayout().layout.listHeight
	if model.cursor != 0 || model.agentCursor != 2 || model.visibleListStart(rows, height) != 3 {
		t.Fatalf(
			"oversized group selection/start = %d/%d/%d, want 0/2/3",
			model.cursor,
			model.agentCursor,
			model.visibleListStart(rows, height),
		)
	}
}

func threeAgentsForTest() []session.AgentSession {
	return []session.AgentSession{{Harness: "pi"}, {Harness: testSupportHarnessCodex}, {Harness: "claude"}}
}

func TestVisibleAgentRowsDoNotStartWithOrphanFromAnotherSession(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{Name: "first", Agents: threeAgentsForTest()},
		{Name: "second", Agents: []session.AgentSession{{Harness: "pi"}, {Harness: testSupportHarnessCodex}}},
		{Name: "selected", Agents: threeAgentsForTest()},
		{Name: "later", Agents: threeAgentsForTest()},
	}
	model.cursor = 2

	rows := model.visibleListRows(5)
	if len(rows) == 0 || rows[0].isAgent() {
		t.Fatalf("visible rows start with orphan child: %#v", rows)
	}
}

func TestTallSelectedAgentGroupStartsAtItsParent(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{Name: "before", Agents: threeAgentsForTest()},
		{Name: "selected", Agents: append(threeAgentsForTest(), threeAgentsForTest()...)},
		{Name: "after"},
	}
	model.cursor = 1

	rows := model.visibleListRows(4)
	if len(rows) != 4 {
		t.Fatalf("visible row count = %d, want 4", len(rows))
	}

	if rows[0].isAgent() || rows[0].sessionIndex != 1 {
		t.Fatalf("first visible row = %#v, want selected parent", rows[0])
	}
}

func TestAgentsModeRegularSessionsSeparatorAndSwitch(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{Name: "agent-session", Agents: []session.AgentSession{{Harness: testSupportHarnessCodex, HarnessSession: "s1", Status: session.AgentStatusRunning}}},
		{Name: "regular-session-1"},
		{Name: "regular-session-2"},
	}

	t.Run("rows include separator", func(t *testing.T) {
		assertMixedAgentSessionRows(t, model)
	})
	t.Run("render includes regular sessions", func(t *testing.T) {
		assertMixedAgentSessionRendering(t, model)
	})
	t.Run("enter switches regular session", func(t *testing.T) {
		assertMixedAgentSessionSwitch(t, model)
	})
}

func assertMixedAgentSessionRows(t *testing.T, model *Model) {
	t.Helper()

	listRows := model.popupListRows()
	if len(listRows) != 5 {
		t.Fatalf("listRows count = %d, want 5: %#v", len(listRows), listRows)
	}

	if !listRows[2].isSeparator() {
		t.Fatalf("listRows[2] is not separator: %#v", listRows[2])
	}

	selectable := model.popupSelectableRows()
	if len(selectable) != 3 {
		t.Fatalf("selectable count = %d, want agent child and two regular sessions: %#v", len(selectable), selectable)
	}
}

func assertMixedAgentSessionRendering(t *testing.T, model *Model) {
	t.Helper()

	output := stripANSI(model.renderList(80, 6))
	if !strings.Contains(output, "Sessions") {
		t.Fatalf("rendered list missing Sessions separator: %q", output)
	}

	if !strings.Contains(output, "regular-session-1") || !strings.Contains(output, "regular-session-2") {
		t.Fatalf("rendered list missing regular sessions: %q", output)
	}
}

func assertMixedAgentSessionSwitch(t *testing.T, model *Model) {
	t.Helper()

	model.moveSelectionByStep(1)

	if got := model.selectedListRow(); got.sessionIndex != 0 || got.agentIndex != 0 {
		t.Fatalf("selected row after step down = %#v, want agent (0, 0)", got)
	}

	model.moveSelectionByStep(1)

	selected := model.selectedListRow()
	if selected.sessionIndex != 1 || selected.isAgent() {
		t.Fatalf("selected row after step down = %#v, want regular-session-1 (1, -1)", selected)
	}

	cmdFn := model.handleEnter()
	if cmdFn == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := cmdFn()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	model.Update(cmd())

	if model.switchAfterQuit != "regular-session-1" {
		t.Fatalf("switchAfterQuit = %q, want regular-session-1", model.switchAfterQuit)
	}
}

func TestAgentStatusGlyphColors(t *testing.T) {
	model := newTestModel()
	model.config.Theme.Success = "#50fa7b"
	model.config.Theme.Secondary = "#ffdd57"
	model.config.Theme.Error = "#ff5555"
	model.config.Theme.Warning = "#ff9e3b"
	model.config.Theme.Muted = "#6c7086"

	tests := []struct {
		name   string
		status session.AgentStatus
		glyph  string
		color  string
	}{
		{name: "running", status: session.AgentStatusRunning, glyph: "●", color: "\x1b[38;2;80;250;123m"},
		{name: "waiting", status: session.AgentStatusWaiting, glyph: "●", color: "\x1b[38;2;255;221;87m"},
		{name: "failed", status: session.AgentStatusFailed, glyph: "●", color: "\x1b[38;2;255;85;85m"},
		{name: "interrupted", status: session.AgentStatusInterrupted, glyph: "●", color: "\x1b[38;2;255;158;59m"},
		{name: "idle", status: session.AgentStatusIdle, glyph: "○", color: "\x1b[38;2;108;112;134m"},
		{name: "unknown", status: session.AgentStatusUnknown, glyph: "○", color: "\x1b[38;2;108;112;134m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := model.agentStatusGlyph(tt.status)
			if !strings.Contains(got, tt.glyph) || !strings.Contains(got, tt.color) {
				t.Fatalf("glyph = %q, want %q with color %q", got, tt.glyph, tt.color)
			}
		})
	}
}

func TestAgentStatusInterruptedCustomWarningColor(t *testing.T) {
	model := newTestModel()
	model.config.Theme.Warning = "#123456"

	const wantColor = "\x1b[38;2;18;52;86m"

	got := model.agentStatusGlyph(session.AgentStatusInterrupted)
	if !strings.Contains(got, "●") || !strings.Contains(got, wantColor) {
		t.Fatalf("interrupted glyph = %q, want ● with custom color %q", got, wantColor)
	}
}

func TestAgentStatusCustomColors(t *testing.T) {
	model := newTestModel()
	model.config.Theme.AgentStatus = &config.AgentStatusThemeConfig{
		Failed: "#ff0077",
	}

	const wantColor = "\x1b[38;2;255;0;119m"

	got := model.agentStatusGlyph(session.AgentStatusFailed)
	if !strings.Contains(got, "●") || !strings.Contains(got, wantColor) {
		t.Fatalf("failed glyph = %q, want ● with custom color %q", got, wantColor)
	}
}

func TestSeparatorOmittedWhenOnlyAgentsOrOnlyRegularSessions(t *testing.T) {
	modelAgentsOnly := newTestModel()
	modelAgentsOnly.displayMode = displayModeAgents

	modelAgentsOnly.filtered = []session.Session{
		{Name: testSupportAgentRegistryID, Agents: []session.AgentSession{{Harness: testSupportHarnessCodex}}},
		{Name: "agent-2", Agents: []session.AgentSession{{Harness: "pi"}}},
	}
	for _, row := range modelAgentsOnly.popupListRows() {
		if row.isSeparator() {
			t.Fatal("separator unexpectedly present when only agent sessions exist")
		}
	}

	modelRegularOnly := newTestModel()
	modelRegularOnly.displayMode = displayModeAgents

	modelRegularOnly.filtered = []session.Session{
		{Name: "regular-1"},
		{Name: "regular-2"},
	}
	for _, row := range modelRegularOnly.popupListRows() {
		if row.isSeparator() {
			t.Fatal("separator unexpectedly present when only regular sessions exist")
		}
	}
}

func TestNumericShortcutsSkipSeparatorInAgentsMode(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 10
	model.filtered = []session.Session{
		{Name: "agent-sess", Agents: []session.AgentSession{{Harness: "pi"}, {Harness: testSupportHarnessCodex}}},
		{Name: "reg-1"},
		{Name: "reg-2"},
	}

	shortcuts := model.visibleShortcutRows()

	wantAgentShortcuts := []popupListRow{
		{sessionIndex: 0, agentIndex: 0},
		{sessionIndex: 0, agentIndex: 1},
	}
	if !slices.Equal(shortcuts, wantAgentShortcuts) {
		t.Fatalf("visible shortcuts for agent session = %#v, want %#v", shortcuts, wantAgentShortcuts)
	}

	model.cursor = 1

	regShortcuts := model.visibleShortcutRows()

	wantRegShortcuts := []popupListRow{
		{sessionIndex: 1, agentIndex: parentAgentIndex},
	}
	if !slices.Equal(regShortcuts, wantRegShortcuts) {
		t.Fatalf("visible shortcuts for regular session = %#v, want %#v", regShortcuts, wantRegShortcuts)
	}
}

func TestAgentMouseClickOnSeparatorIsNoOp(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 10
	model.filtered = []session.Session{
		{Name: "agent-sess", Agents: []session.AgentSession{{Harness: "pi"}}},
		{Name: "reg-1"},
	}
	model.cursor = 0
	model.agentCursor = 0

	layout := model.popupMouseLayout()
	separatorY := layout.listY + 2

	model = updateTestModel(t, model, mouseClick(layout.bodyX+5, separatorY, tea.MouseLeft))

	selected := model.selectedListRow()
	if selected.sessionIndex != 0 || selected.agentIndex != 0 {
		t.Fatalf("selection after clicking separator = %#v, want unchanged (0, 0)", selected)
	}
}

func TestStepNavigationWrapsAcrossAgentsAndRegularSessions(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{Name: "agent-sess", Agents: []session.AgentSession{{Harness: "pi"}}},
		{Name: "reg-1"},
	}
	model.cursor = 0
	model.agentCursor = 0

	model.moveSelectionByStep(1)

	if got := model.selectedListRow(); got.sessionIndex != 1 || got.isAgent() {
		t.Fatalf("selection after step down = %#v, want reg-1 (1, -1)", got)
	}

	model.moveSelectionByStep(1)

	if got := model.selectedListRow(); got.sessionIndex != 0 || got.agentIndex != 0 {
		t.Fatalf("selection after wrap step down = %#v, want agent pi (0, 0)", got)
	}

	model.moveSelectionByStep(-1)

	if got := model.selectedListRow(); got.sessionIndex != 1 || got.isAgent() {
		t.Fatalf("selection after wrap step up = %#v, want reg-1 (1, -1)", got)
	}
}

func TestAgentHarnessFormatWithLongHarnessName(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:       "long-harness",
		AgentTotal: 2,
		Agents: []session.AgentSession{
			{Harness: "copilot", HarnessSession: "copilot-agent", Status: session.AgentStatusRunning},
			{Harness: "kimi-code", HarnessSession: "kimi-agent", Status: session.AgentStatusIdle},
		},
	}}

	output := stripANSI(model.renderList(80, 3))
	if !strings.Contains(output, "Copilot") || !strings.Contains(output, "Kimi-code") {
		t.Fatalf("rendered list missing long harness names: %q", output)
	}
}
