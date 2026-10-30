package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	pickerEvidenceWriteEnvironment = "SESH_WRITE_TUI_EVIDENCE"
	pickerEvidenceDirectory        = "../../.agents/tui-evidence"
)

func TestPickerRichAgentVisualContract(t *testing.T) {
	model := newRichAgentVisualModel(t)

	for _, viewport := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "full", width: 160, height: 45},
		{name: "narrow", width: 80, height: 24},
	} {
		t.Run(viewport.name, func(t *testing.T) {
			model.width = viewport.width
			model.height = viewport.height

			rendered := viewString(model.View())
			plain := stripANSI(rendered)

			assertVisualViewport(t, rendered, viewport.width, viewport.height)
			assertVisualText(t, plain, "sesh", "api", "docs", "shell")
			assertVisualText(t, plain, "main", "+413", "-102", "↑2", "↓1")
			assertVisualText(t, plain, "enter select", "AGENTS")

			if !strings.Contains(rendered, ansiDefaultSelectedBg) {
				t.Fatalf("rich picker view has no selected-row background: %q", rendered)
			}

			if viewport.width == 160 {
				assertVisualText(t, plain, "Fix native restore receipt", "Codex")
				assertVisualText(t, plain, "↑/↓ agents", "ctrl+↑/↓ sessions")
				assertAlignedVisualColumn(t, plain, "\ue725")
			} else {
				assertVisualText(t, plain, "…", "↑/↓ agent", "⌃↑/↓ session")
			}

			divider := dividerLineIndex(plain)
			if divider < 0 || lineIndexOf(plain, "api") >= divider || divider >= lineIndexOf(plain, "docs") {
				t.Errorf("agent groups, divider, and ordinary rows are out of order")
			}
		})
	}
}

func TestPickerRichAgentDialogsPreserveUnderlyingVisualContract(t *testing.T) {
	model := newRichAgentVisualModel(t)
	model.width = 80
	model.height = 24

	model.showHelp = true
	help := viewString(model.View())
	assertVisualViewport(t, help, 80, 24)
	assertVisualText(t, stripANSI(help), "Popup Keys", "Open selected", "Rename session", "Kill session", "Create session")

	model.showHelp = false
	openTestKillConfirm(model)
	confirm := viewString(model.View())
	assertVisualViewport(t, confirm, 80, 24)
	assertVisualText(t, stripANSI(confirm), "Confirm", "Kill session", "Yes", "No")
}

func TestWritePickerVisualEvidence(t *testing.T) {
	if os.Getenv(pickerEvidenceWriteEnvironment) != "1" {
		t.Skip("set " + pickerEvidenceWriteEnvironment + "=1 to write ANSI evidence")
	}

	if err := os.MkdirAll(pickerEvidenceDirectory, 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}

	root, err := os.OpenRoot(pickerEvidenceDirectory)
	if err != nil {
		t.Fatalf("open evidence directory: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close evidence directory: %v", closeErr)
		}
	})

	model := newRichAgentVisualModel(t)
	fixtures := []struct {
		name    string
		width   int
		height  int
		confirm bool
		help    bool
	}{
		{name: "picker-agents-160x45.ansi", width: 160, height: 45},
		{name: "picker-agents-80x24.ansi", width: 80, height: 24},
		{name: "picker-help-80x24.ansi", width: 80, height: 24, help: true},
		{name: "picker-kill-80x24.ansi", width: 80, height: 24, confirm: true},
	}

	for _, fixture := range fixtures {
		model.width = fixture.width
		model.height = fixture.height
		model.closeActionDialog()

		if fixture.confirm {
			openTestKillConfirm(model)
		}

		model.showHelp = fixture.help

		if err := root.WriteFile(fixture.name, []byte(viewString(model.View())), 0o600); err != nil {
			t.Fatalf("write %s: %v", fixture.name, err)
		}
	}
}

func newRichAgentVisualModel(t *testing.T) *Model {
	t.Helper()

	cfg := config.DefaultConfig()

	model := newTestModelWithConfig(t, cfg)
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.cursor = 0
	model.agentCursor = 0

	now := time.Now()
	model.filtered = []session.Session{
		{
			ID:           "$1",
			Name:         "0",
			Kind:         session.KindLive,
			Current:      true,
			ActivePaneID: "%1",
			AgentTotal:   2,
			AgentActive:  1,
			Agents: []session.AgentSession{
				{
					PositionKnown:    true,
					RegistryID:       "01a0c31f-e870-7660-bd1a-074abe17fcc6",
					Harness:          "codex",
					HarnessSession:   "Fix native restore receipt",
					PaneID:           "%1",
					CWD:              "/work/sesh",
					ProjectRoot:      "/work/sesh",
					GitBranch:        "main",
					Worktree:         "/work/sesh",
					GitChanges:       "+413 -102",
					GitAhead:         2,
					GitBehind:        1,
					GitTrackingKnown: true,
					Status:           session.AgentStatusRunning,
					StatusSince:      now.Add(-2 * time.Minute),
				},
				{
					PositionKnown:    true,
					RegistryID:       "01a0c51d-632e-7000-b562-5d6cbcc877e1",
					Harness:          "omp",
					HarnessSession:   "Review picker visual parity",
					PaneID:           "%2",
					CWD:              "/work/sesh/internal/tui",
					ProjectRoot:      "/work/sesh",
					GitBranch:        "feature/picker-parity",
					Worktree:         "/work/sesh",
					GitChanges:       "+24 -5",
					GitTrackingKnown: true,
					Status:           session.AgentStatusIdle,
					StatusSince:      now.Add(-18 * time.Minute),
				},
			},
		},
		{
			ID:          "$2",
			Name:        "api",
			Kind:        session.KindLive,
			AgentTotal:  1,
			AgentFailed: 1,
			Agents: []session.AgentSession{{
				PositionKnown:    true,
				RegistryID:       "01a0c325-140f-7000-9fb0-ffe4329deaad",
				Harness:          "pi",
				HarnessSession:   "Investigate lifecycle boundary",
				PaneID:           "%7",
				CWD:              "/work/api",
				ProjectRoot:      "/work/api",
				GitBranch:        "release",
				Worktree:         "/work/api",
				GitChanges:       "+7 -3",
				GitTrackingKnown: true,
				Status:           session.AgentStatusFailed,
				StatusSince:      now.Add(-time.Hour - 33*time.Minute),
			}},
		},
		{ID: "$3", Name: "docs", Kind: session.KindLive, Activity: now.Add(-5 * time.Minute)},
		{ID: "$4", Name: "shell", Kind: session.KindLive, Activity: now.Add(-time.Hour)},
	}

	return model
}

func assertVisualViewport(t *testing.T, rendered string, width int, height int) {
	t.Helper()

	lines := strings.Split(rendered, "\n")
	if len(lines) > height {
		t.Fatalf("rendered height = %d, want <= %d", len(lines), height)
	}

	for index, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("rendered line %d width = %d, want %d: %q", index, got, width, line)
		}
	}
}

func assertVisualText(t *testing.T, rendered string, values ...string) {
	t.Helper()

	for _, value := range values {
		if !strings.Contains(rendered, value) {
			t.Errorf("rendered view does not contain %q", value)
		}
	}
}

func assertAlignedVisualColumn(t *testing.T, rendered string, marker string) {
	t.Helper()

	want := -1
	matched := 0

	for line := range strings.SplitSeq(rendered, "\n") {
		prefix, _, found := strings.Cut(line, marker)
		if !found {
			continue
		}

		column := lipgloss.Width(prefix)

		matched++

		if want < 0 {
			want = column
			continue
		}

		if column != want {
			t.Errorf("column %q starts at %d, want %d in %q", marker, column, want, line)
		}
	}

	if matched < 2 {
		t.Fatalf("found %d rows with column %q, want at least 2", matched, marker)
	}
}

func dividerLineIndex(text string) int {
	for index, line := range strings.Split(text, "\n") {
		inner := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
		if inner != "" && strings.Trim(inner, "─") == "" {
			return index
		}
	}

	return -1
}

func lineIndexOf(text, substring string) int {
	for index, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substring) {
			return index
		}
	}

	return -1
}
