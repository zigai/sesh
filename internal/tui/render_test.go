package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

func TestRenderInputBarHidesIndicators(t *testing.T) {
	model := newTestModel()
	output := stripANSI(model.renderInputBar(80))

	for _, label := range []string{"fuzzy", "popup", "inline", "full"} {
		if strings.Contains(output, label) {
			t.Fatalf("renderInputBar output = %q, should not contain %q", output, label)
		}
	}
}

func TestRenderFrameShowsStyledDisplayModeInBottomRight(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 48
	model.height = 10

	output := model.renderFrame()
	lines := strings.Split(stripANSI(output), "\n")

	bottom := lines[len(lines)-1]
	if strings.Contains(bottom, "AGENTS") {
		t.Fatalf("bottom border = %q, want uninterrupted border", bottom)
	}

	footer := lines[len(lines)-2]
	if !strings.Contains(footer, "AGENTS") || strings.Index(footer, "AGENTS") < len(footer)-12 {
		t.Fatalf("footer = %q, want AGENTS badge at right", footer)
	}

	wantBadge := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color(model.config.Theme.Primary)).
		Bold(true).
		Render(" AGENTS ")
	if !strings.Contains(output, wantBadge) {
		t.Fatalf("renderFrame() missing black-on-primary AGENTS badge: %q", output)
	}
}

func TestRenderInputBarAlignsQueryWithSessionNames(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("dev")

	input := stripANSI(model.renderInputBar(32))

	before, _, ok := strings.Cut(input, "dev")
	if !ok {
		t.Fatalf("input bar = %q, expected query text", input)
	}

	inputWidth := lipgloss.Width(before)

	layout := model.calcSessionLayout(32)
	row := stripANSI(model.renderSessionRow(layout, session.Session{Name: testSupportSessionDevName}, true))

	before0, _, ok0 := strings.Cut(row, "dev")
	if !ok0 {
		t.Fatalf("session row = %q, expected session name", row)
	}

	rowWidth := lipgloss.Width(before0)

	if inputWidth != rowWidth {
		t.Fatalf("input query starts at column %d, session name starts at column %d", inputWidth, rowWidth)
	}
}

func TestViewFrameKeepsRightBorderAcrossStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
	}{
		{
			name: "normal list",
			setup: func(model *Model) {
				model.filtered = []session.Session{{Name: testSupportSessionDevName}}
			},
		},
		{
			name: "empty state",
			setup: func(model *Model) {
				model.filtered = nil
			},
		},
		{
			name: "status",
			setup: func(model *Model) {
				model.filtered = []session.Session{{Name: testSupportSessionDevName}}
				model.setErrorStatus("tmux unavailable")
			},
		},
		{
			name: "dialog overlay",
			setup: func(model *Model) {
				model.filtered = []session.Session{{Name: testSupportSessionDevName}}
				openTestKillConfirm(model)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newTestModel()
			model.width = 80
			model.height = 20
			tt.setup(model)

			assertRightBorder(t, viewString(model.View()), 80)
		})
	}
}

func TestLoadingViewDoesNotRenderFalseEmptyState(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 20
	model.loadingSessions = true

	loading := stripANSI(viewString(model.View()))
	if !strings.Contains(loading, "Loading sessions...") {
		t.Fatalf("loading view = %q, want loading status", loading)
	}

	if strings.Contains(loading, "No live agent sessions found") {
		t.Fatalf("loading view = %q, should not claim the unfinished result is empty", loading)
	}

	model.handleSessionLoadResult(sessionLoadResultMsg{})

	loaded := stripANSI(viewString(model.View()))
	if strings.Contains(loaded, "Loading sessions...") || !strings.Contains(loaded, "No live agent sessions found") {
		t.Fatalf("loaded empty view = %q, want the true empty state after loading", loaded)
	}
}

func TestPopupInitialCatalogRendersWithoutLoadingFrame(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 80
	model.height = 20
	model.Init()

	model.handleSessionLoadResult(sessionLoadResultMsg{
		sessions: []session.Session{{ID: "$1", Name: testSupportSessionDevName, Kind: session.KindLive}},
		status:   session.StatusComplete,
	})

	view := stripANSI(viewString(model.View()))
	if !strings.Contains(view, testSupportSessionDevName) || strings.Contains(view, "Loading sessions...") {
		t.Fatalf("loaded popup view = %q, want session row without loading frame", view)
	}

	updateTestModel(t, model, popupStartupDeadlineMsg{})

	if got := stripANSI(viewString(model.View())); got != view {
		t.Fatalf("late startup deadline changed the completed view = %q, want %q", got, view)
	}
}

func TestPopupStartupLoadingHidesIncompleteOriginRows(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 20
	model.Init()
	updateTestModel(t, model, originSessionsMsg{rows: []session.Session{{
		ID: "$2", Name: "origin-agent-session", Kind: session.KindLive, Origin: session.OriginBridge, AgentTotal: 1,
	}}})
	updateTestModel(t, model, popupStartupDeadlineMsg{})

	for _, rendered := range []string{viewString(model.View()), model.renderList(76, 12)} {
		view := stripANSI(rendered)
		if !strings.Contains(view, "Loading sessions...") || strings.Contains(view, "origin-agent-session") ||
			strings.Contains(view, "No live agent sessions found") {
			t.Fatalf("startup view = %q, want loading without incomplete origin rows", view)
		}
	}

	model.handleSessionLoadResult(sessionLoadResultMsg{status: session.StatusComplete})

	view := stripANSI(viewString(model.View()))
	if !strings.Contains(view, "origin-agent-session") || strings.Contains(view, "Loading sessions...") {
		t.Fatalf("completed startup view = %q, want observed origin row", view)
	}
}

func TestPopupInitialCatalogFailureReplacesBlankView(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 80
	model.height = 20
	model.Init()

	model.handleSessionLoadResult(sessionLoadResultMsg{
		status:     session.StatusFailed,
		catalogErr: errTmuxUnavailable,
	})

	view := stripANSI(viewString(model.View()))
	if !strings.Contains(view, "Failed to load sessions") || strings.Contains(view, "Loading sessions...") {
		t.Fatalf("failed popup view = %q, want failure rather than loading state", view)
	}
}

func TestExpandedAgentFrameKeepsFirstBlankRowRightBorder(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.height = 15
	model.filtered = []session.Session{
		{
			Name:       "pi-codex-look",
			AgentTotal: 1,
			Agents: []session.AgentSession{{
				Harness: "pi", HarnessSession: "0194fede-d390-7d33-97d2-bba6383bec71",
				Status: session.AgentStatusIdle, StatusSince: time.Now().Add(-51 * time.Minute),
			}},
		},
		{
			Name:        "pi-typed-commands",
			AgentTotal:  1,
			AgentActive: 1,
			Agents: []session.AgentSession{{
				Harness: "pi", HarnessSession: "019f6aa1-db20-75c3-bebe-1658114ddc6f",
				Status: session.AgentStatusRunning, StatusSince: time.Now().Add(-2 * time.Hour),
			}},
		},
	}

	for _, width := range []int{62, 96, 151} {
		model.width = width
		assertRightBorder(t, viewString(model.View()), width)
	}
}

func TestViewFrameKeepsTopCornersAtNarrowWidth(t *testing.T) {
	model := newTestModel()
	model.width = 30
	model.height = 12
	model.filtered = make([]session.Session, 123456)

	assertRightBorder(t, viewString(model.View()), 30)
}

func TestViewUsesAltScreenForTmuxPopup(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 120
	model.height = 24
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	view := model.View()
	if !view.AltScreen {
		t.Fatal("View().AltScreen = false, want true for tmux popup rendering")
	}

	if view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("View().MouseMode = %v, want MouseModeAllMotion", view.MouseMode)
	}
}

func TestViewFitsTmuxPopupFrameToViewport(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 80
	model.height = 20

	model.filtered = make([]session.Session, 123456)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: fmt.Sprintf("session-%d", i)}
	}

	output := stripANSI(viewString(model.View()))

	lines := strings.Split(output, "\n")
	if len(lines) > model.height {
		t.Fatalf("popup output has %d lines, want <= %d: %q", len(lines), model.height, output)
	}

	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Fatalf("popup frame top border = %q, want frame to start at viewport edge", lines[0])
	}

	for i, line := range lines {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("popup line %d width = %d, want <= %d: %q", i, got, model.width, line)
		}
	}
}

func TestViewFitsNarrowPhonePopupViewport(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 18
	model.height = 8
	model.filtered = []session.Session{{Name: "phone-session-with-a-long-name"}}

	output := stripANSI(viewString(model.View()))

	lines := strings.Split(output, "\n")
	if len(lines) > model.height {
		t.Fatalf("phone popup output has %d lines, want <= %d: %q", len(lines), model.height, output)
	}

	for i, line := range lines {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("phone popup line %d width = %d, want <= %d: %q", i, got, model.width, line)
		}
	}
}

func TestViewUsesCompactLabelBelowMinimumFrameSize(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.width = 4
	model.height = 3

	output := stripANSI(viewString(model.View()))
	if output != "AGEN" {
		t.Fatalf("tiny popup view = %q, want compact display-mode label", output)
	}
}

func TestCalcPopupLayoutTightHeightHidesFooter(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 4

	layout := model.calcPopupLayout(false)
	if layout.showFooter {
		t.Fatal("showFooter = true, want false for tight height")
	}

	if layout.listHeight != 1 {
		t.Fatalf("listHeight = %d, want 1", layout.listHeight)
	}
}

func TestCalcPopupLayoutHidesFooterHintsButKeepsModeWhenDisabled(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 12

	withFooter := model.calcPopupLayout(false)
	if !withFooter.showFooter {
		t.Fatal("showFooter = false, want true by default")
	}

	if !withFooter.showFooterHints {
		t.Fatal("showFooterHints = false, want true by default")
	}

	model.config.Settings.ShowFooter = false

	withoutFooter := model.calcPopupLayout(false)
	if !withoutFooter.showFooter {
		t.Fatal("showFooter = false, want mode row when hints are disabled")
	}

	if withoutFooter.showFooterHints {
		t.Fatal("showFooterHints = true, want false when disabled")
	}

	if withoutFooter.listHeight != withFooter.listHeight {
		t.Fatalf("listHeight without hints = %d, want %d", withoutFooter.listHeight, withFooter.listHeight)
	}
}

func TestRenderListOmitsNameHeader(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	output := stripANSI(model.renderList(40, 2))
	if strings.Contains(output, "name") {
		t.Fatalf("renderList output = %q, should not include name header", output)
	}

	if !strings.Contains(output, "dev") {
		t.Fatalf("renderList output = %q, expected session name", output)
	}
}

func TestRenderFooterCompactsAtNarrowWidth(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	rendered := model.renderFooter(30)

	output := stripANSI(rendered)
	if !strings.Contains(output, "? help") {
		t.Fatalf("renderFooter output = %q, expected compact help", output)
	}

	if strings.Contains(output, "sessions:") {
		t.Fatalf("renderFooter output = %q, should not include session count", output)
	}

	if strings.Contains(output, "\n") {
		t.Fatalf("renderFooter output = %q, should stay on one line", output)
	}

	if got := lipgloss.Width(rendered); got > 30 {
		t.Fatalf("renderFooter width = %d, want <= 30", got)
	}

	if strings.Contains(output, "rename") {
		t.Fatalf("renderFooter output = %q, should omit long hints at narrow width", output)
	}
}

func TestRenderFooterUsesShortcutKeyColor(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	rendered := model.renderFooter(80)
	want := lipgloss.NewStyle().
		Foreground(lipgloss.Color("12")).
		Bold(true).
		Inline(true).
		Render("enter")

	if !strings.Contains(rendered, want) {
		t.Fatalf("renderFooter shortcut color missing: got %q, want styled enter %q", rendered, want)
	}
}

func TestRenderFooterShowsHelpShortcutLast(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName}}

	output := stripANSI(model.renderFooter(160))

	if !strings.Contains(output, "? help") {
		t.Fatalf("renderFooter output = %q, expected help", output)
	}

	if strings.Contains(output, "sessions:") {
		t.Fatalf("renderFooter output = %q, should not include session count", output)
	}

	if strings.TrimSpace(output)[len(strings.TrimSpace(output))-len("help"):] != "help" {
		t.Fatalf("renderFooter output = %q, want help shortcut last", output)
	}
}

func TestRenderFooterTruncatesCompactHelpAtVeryNarrowWidth(t *testing.T) {
	model := newTestModel()
	model.filtered = make([]session.Session, 123456)

	rendered := model.renderFooter(6)

	output := stripANSI(rendered)
	if strings.Contains(output, "\n") {
		t.Fatalf("renderFooter output = %q, should stay on one line", output)
	}

	if got := lipgloss.Width(rendered); got > 6 {
		t.Fatalf("renderFooter width = %d, want <= 6", got)
	}

	if !strings.HasSuffix(rendered, "\x1b[0m") {
		t.Fatalf("renderFooter output should close truncated ANSI styling: %q", rendered)
	}
}

func TestRenderFooterOmitsLiveOnlyActionsForSavedRows(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: "archive", Kind: session.KindSaved}}

	output := stripANSI(model.renderFooter(96))
	for _, unwanted := range []string{"rename", "kill"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("renderFooter output = %q, should omit %q for saved rows", output, unwanted)
		}
	}

	if strings.Contains(output, "sessions:") {
		t.Fatalf("renderFooter output = %q, should not include session count", output)
	}

	for _, want := range []string{"select", "delete"} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderFooter output = %q, expected %q", output, want)
		}
	}
}

func TestRenderAgentsFooterKeepsSessionActionsForSelectedChild(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:   "project",
		Kind:   session.KindLive,
		Agents: []session.AgentSession{{RegistryID: "agent-1", PaneID: "%1"}},
	}}
	model.agentCursor = 0

	output := stripANSI(model.renderFooter(200))
	for _, want := range []string{"↑/↓ agents", "ctrl+↑/↓ sessions", "alt+r rename", "alt+x kill"} {
		if !strings.Contains(output, want) {
			t.Fatalf("agent footer = %q, want %q", output, want)
		}
	}

	if strings.Contains(output, "delete") {
		t.Fatalf("agent footer broadened delete action: %q", output)
	}

	compact := stripANSI(model.renderFooter(96))
	for _, want := range []string{"↑/↓ agents", "ctrl+↑/↓ sessions", "alt+r rename", "alt+x kill"} {
		if !strings.Contains(compact, want) {
			t.Fatalf("96-column child footer = %q, want %q", compact, want)
		}
	}

	modeFooter := stripANSI(model.renderModeFooter(96, true))
	for _, want := range []string{"alt+r rename", "alt+x kill", "AGENTS"} {
		if !strings.Contains(modeFooter, want) {
			t.Fatalf("96-column mode footer = %q, want %q", modeFooter, want)
		}
	}
}

func TestRenderNarrowAgentsFooterKeepsNavigationAndHelp(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{Name: "project", Agents: []session.AgentSession{{RegistryID: "agent-1"}}}}

	output := stripANSI(model.renderFooter(50))
	for _, want := range []string{"↑/↓ agent", "? help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("narrow agent footer = %q, want %q", output, want)
		}
	}

	if width := lipgloss.Width(model.renderFooter(50)); width > 50 {
		t.Fatalf("narrow agent footer width = %d, want <= 50", width)
	}
}

func TestRenderSessionRowSelectedPadsFullWidth(t *testing.T) {
	model := newTestModel()
	layout := model.calcSessionLayout(32)

	rendered := model.renderSessionRow(layout, session.Session{Name: testSupportSessionDevName}, true)

	output := stripANSI(rendered)
	if got := lipgloss.Width(output); got != 32 {
		t.Fatalf("selected row width = %d, want 32: %q", got, output)
	}

	if !strings.HasPrefix(output, defaultSelectionBar+"dev") {
		t.Fatalf("selected row output = %q, want selection bar followed by session name", output)
	}

	wantBar := lipgloss.NewStyle().
		Foreground(lipgloss.Color(shortcutKeyColor)).
		Background(lipgloss.Color(model.config.Theme.Selected)).
		Bold(true).
		Render(defaultSelectionBar)
	if !strings.HasPrefix(rendered, wantBar) {
		t.Fatalf("selected row marker style = %q, want shortcut color marker %q", rendered, wantBar)
	}
}

func TestSelectedAgentRightColumnKeepsBackgroundAroundStyledStatus(t *testing.T) {
	model := newTestModel()
	layout := sessionTableLayout{rightWidth: 4}
	styles := model.sessionRowStyles(layout)
	line := display.Line{Right: []display.Part{{
		Value: "\x1b[38;5;244m○\x1b[0m 1m",
	}}}

	var rendered strings.Builder
	model.writeSessionFormatRight(&rendered, line, layout, true, true, styles)

	want := ansiDefaultSelectedBg + " \x1b[38;5;244m○\x1b[0m" + ansiDefaultSelectedBg + " 1m" + ansiReset
	if got := rendered.String(); got != want {
		t.Fatalf("selected agent right column = %q, want %q", got, want)
	}
}

func TestSelectedAgentRowPreservesStatusGlyphColor(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.agentCursor = 0
	model.config.Theme.Success = "#50fa7b"
	model.filtered = []session.Session{{
		Name:       "project",
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness: "pi", HarnessSession: "agent-id", PaneID: "%7", Status: session.AgentStatusRunning,
		}},
	}}

	output := model.renderList(60, 2)

	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		t.Fatalf("rendered lines count = %d, want >= 2", len(lines))
	}

	childLine := lines[1]
	if !strings.Contains(childLine, "\x1b[38;2;80;250;123m●\x1b[m") {
		t.Fatalf("selected agent child row = %q, want green ● glyph with RGB escape codes", childLine)
	}

	if !strings.Contains(childLine, ansiDefaultSelectedBg) {
		t.Fatalf("selected agent child row = %q, want selected background", childLine)
	}
}

func TestSelectedAgentRowPreservesStatusGlyphColorWithCustomTheme(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.agentCursor = 0
	model.config.Theme.Selected = "#123456"
	model.config.Theme.Success = "#50fa7b"
	model.filtered = []session.Session{{
		Name:       "project",
		AgentTotal: 1,
		Agents: []session.AgentSession{{
			Harness: "pi", HarnessSession: "agent-id", PaneID: "%7", Status: session.AgentStatusRunning,
		}},
	}}

	output := model.renderList(60, 2)

	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		t.Fatalf("rendered lines count = %d, want >= 2", len(lines))
	}

	childLine := lines[1]
	if !strings.Contains(childLine, "\x1b[38;2;80;250;123m●\x1b[m") {
		t.Fatalf("selected agent child row = %q, want green ● glyph with RGB escape codes", childLine)
	}

	if !strings.Contains(childLine, "\x1b[48;2;18;52;86m") {
		t.Fatalf("selected agent child row = %q, want custom selected background", childLine)
	}
}

func TestRenderSessionRowWithCustomForeground(t *testing.T) {
	model := newTestModel()
	model.config.Theme.Foreground = "#123456"
	model.filtered = []session.Session{
		{Name: "selected-row"},
		{Name: "unselected-row"},
	}

	output := model.renderList(60, 2)

	const wantColor = "\x1b[38;2;18;52;86m"
	if !strings.Contains(output, wantColor) {
		t.Fatalf("rendered output = %q, want custom foreground color %q", output, wantColor)
	}
}

func TestRenderSessionRowDoesNotShowAttachedBadge(t *testing.T) {
	model := newTestModel()
	layout := model.calcSessionLayout(48)

	output := stripANSI(model.renderSessionRow(layout, session.Session{Name: "dev", Attached: true}, false))
	if strings.Contains(output, "[attached]") {
		t.Fatalf("renderSessionRow output = %q, should not show attached badge", output)
	}
}

func createShortcutTestModel(t *testing.T, style string) *Model {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Settings.ShortcutStyle = style
	model := newTestModelWithConfig(t, cfg)
	model.width = 60
	model.height = 10
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{
			Name: "session-a",
			Agents: []session.AgentSession{
				{Harness: "pi", PaneID: "%1"},
				{Harness: "pi", PaneID: "%2"},
			},
		},
		{
			Name: "session-b",
			Agents: []session.AgentSession{
				{Harness: "pi", PaneID: "%3"},
			},
		},
	}
	model.cursor = 0
	model.agentCursor = 0

	formats, err := compileSessionRowFormats(cfg.Display, cfg.Settings)
	if err != nil {
		t.Fatalf("compileSessionRowFormats failed: %v", err)
	}

	model.sessionFormats = formats

	return model
}

func TestRenderListDisplaysShortcutStyleNone(t *testing.T) {
	model := createShortcutTestModel(t, "none")
	output := stripANSI(model.renderList(60, 5))

	lines := strings.Split(output, "\n")
	if len(lines) != 5 {
		t.Fatalf("renderList() lines count = %d, want 5", len(lines))
	}

	if !strings.HasPrefix(lines[1], "▌ ├─") {
		t.Fatalf("agent 0 line = %q, want '▌ ├─'", lines[1][:6])
	}

	if !strings.HasPrefix(lines[2], "  └─") {
		t.Fatalf("agent 1 line = %q, want '  └─'", lines[2][:6])
	}
}

func TestRenderListDisplaysShortcutStyleIndent(t *testing.T) {
	model := createShortcutTestModel(t, "indent")
	model.focusedSession = model.filtered[0].RuntimeKey()
	output := stripANSI(model.renderList(60, 5))

	lines := strings.Split(output, "\n")
	if len(lines) != 5 {
		t.Fatalf("renderList() lines count = %d, want 5", len(lines))
	}

	if !strings.HasPrefix(lines[1], "a   ") {
		t.Fatalf("agent 0 line = %q, want its key a in the first column", lines[1])
	}

	if !strings.HasPrefix(lines[2], "s   ") {
		t.Fatalf("agent 1 line = %q, want its key s in the first column", lines[2])
	}

	if !strings.HasPrefix(lines[4], "    ") {
		t.Fatalf("unfocused session agent line = %q, want no key", lines[4])
	}
}

func TestRenderListDisplaysShortcutStyleRight(t *testing.T) {
	model := createShortcutTestModel(t, "right")
	model.focusedSession = model.filtered[0].RuntimeKey()
	output := stripANSI(model.renderList(60, 5))

	lines := strings.Split(output, "\n")
	if len(lines) != 5 {
		t.Fatalf("renderList() lines count = %d, want 5", len(lines))
	}

	if !strings.HasSuffix(strings.TrimRight(lines[1], " "), " a") {
		t.Fatalf("agent 0 line missing right key a: %q", lines[1])
	}

	if !strings.HasSuffix(strings.TrimRight(lines[2], " "), " s") {
		t.Fatalf("agent 1 line missing right key s: %q", lines[2])
	}

	if strings.ContainsAny(lines[4], "123") {
		t.Fatalf("unfocused session agent should not have a number: %q", lines[4])
	}
}

func TestStatusMinimalBadgeRenderingAndInteractions(t *testing.T) {
	model := newTestModel()
	model.width = 80
	model.height = 20
	model.filtered = []session.Session{
		{Name: "sess-1"},
		{Name: "sess-2"},
	}

	model.setDegradedSource(degradedSourceCatalog, "Session list is incomplete: some sources could not be read")

	view := stripANSI(viewString(model.View()))

	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.Contains(line, ">") && i+1 < len(lines) {
			if strings.Contains(lines[i+1], "Session list is incomplete") {
				t.Fatalf("status banner unexpectedly rendered directly below input bar: %q", lines[i+1])
			}
		}
	}

	if !strings.Contains(view, "⚠") {
		t.Fatalf("view does not contain minimal warning badge: %q", view)
	}

	model.setErrorStatus("command execution failed")

	errorView := stripANSI(viewString(model.View()))
	if !strings.Contains(errorView, "✖") {
		t.Fatalf("view does not contain minimal error badge: %q", errorView)
	}

	model.showHelp = true

	helpView := stripANSI(viewString(model.View()))
	if !strings.Contains(helpView, "command execution failed") {
		t.Fatalf("help overlay does not contain status message: %q", helpView)
	}

	model.showHelp = false

	layout := model.popupMouseLayout()
	badgeX := layout.bodyX + max(layout.layout.bodyWidth-lipgloss.Width(model.renderFooterBadges()), 0)

	clickHandler, handled := model.handleFooterBadgeClick(tea.Mouse{
		X: badgeX,
		Y: layout.footerY,
	}, layout)
	if !handled || clickHandler == nil {
		t.Fatal("clicking status badge was not handled")
	}

	clickHandler()

	if model.hasStatusBadge() {
		t.Fatalf("status badge still active after click dismiss: level=%v message=%q", model.statusLevel, model.statusMessage)
	}
}

func BenchmarkRenderListLargeVisibleWindow(b *testing.B) {
	model := newTestModel()
	model.width = 100
	model.height = 34

	model.filtered = make([]session.Session, 5000)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: fmt.Sprintf("workspace_%04d_api", i)}
	}

	model.cursor = 2500

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		rendered := model.renderList(96, 28)
		if !strings.Contains(rendered, "workspace_2500_api") {
			b.Fatal("renderList() did not render selected row")
		}
	}
}

func BenchmarkPopupViewWithRowsWhileRefreshing(b *testing.B) {
	model := newTestModel()
	model.mode = modePopup
	model.width = 100
	model.height = 34
	model.loadingSessions = true

	model.filtered = make([]session.Session, 5000)
	for i := range model.filtered {
		model.filtered[i] = session.Session{Name: fmt.Sprintf("workspace_%04d_api", i)}
	}

	model.cursor = 2500

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		rendered := viewString(model.View())
		if !strings.Contains(rendered, "workspace_2500_api") {
			b.Fatal("View() did not render the selected session")
		}
	}
}
