package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

func TestSeshDisplayFormatControlsFieldsAndAlignment(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.SeshFormat = "{current_marker}{session_name} [{active_window_name}] {active_pane_path_name} windows={window_count} {attached}{right}{agent_working}/{agent_total}"
	model := newTestModelWithConfig(t, cfg)
	model.filtered = []session.Session{
		{
			Name:             testSupportSessionDevName,
			Kind:             session.KindLive,
			WindowCount:      4,
			Attached:         true,
			IsCurrent:        true,
			ActiveWindowName: "editor",
			ActivePanePath:   "/work/dev",
			AgentTotal:       3,
			AgentActive:      2,
		},
		{Name: "saved", Kind: session.KindSaved, WindowCount: 1},
	}

	lines := strings.Split(stripANSI(model.renderList(72, 2)), "\n")
	if !strings.Contains(lines[0], "* dev [editor] dev windows=4 attached") || !strings.HasSuffix(lines[0], "2/3") {
		t.Fatalf("custom live row = %q", lines[0])
	}

	if !strings.Contains(lines[1], "  saved []  windows=1 ") || !strings.HasSuffix(lines[1], "0/0") {
		t.Fatalf("custom saved row = %q", lines[1])
	}

	for index, line := range lines {
		if width := lipgloss.Width(line); width != 72 {
			t.Fatalf("line %d width = %d, want 72: %q", index, width, line)
		}
	}
}

func TestSessionFieldFormatsAlignByTerminalDisplayWidth(t *testing.T) {
	const wideName = "\u958b\u767a"

	cfg := config.DefaultConfig()
	cfg.Display.SeshFormat = "{session_name:<8.8}|{active_window_name:^10.10}|{window_count:>3}{right}{session_kind:>6.6}"
	model := newTestModelWithConfig(t, cfg)
	model.filtered = []session.Session{{
		Name:             wideName,
		Kind:             session.KindLive,
		WindowCount:      4,
		ActiveWindowName: "editor",
	}}

	line := stripANSI(model.renderList(56, 1))
	if !strings.Contains(line, wideName+"    |  editor  |  4") {
		t.Fatalf("aligned row = %q, want field-width alignment", line)
	}

	if !strings.HasSuffix(line, "  live") {
		t.Fatalf("aligned row = %q, want right-aligned session kind", line)
	}

	if width := lipgloss.Width(line); width != 56 {
		t.Fatalf("aligned row width = %d, want 56: %q", width, line)
	}
}

func TestSessionFieldMaximumWidthTruncatesBeforePadding(t *testing.T) {
	left := alignSessionFormatPart(display.Part{
		Value:       "abcdefghijklmnop",
		Alignment:   display.AlignmentLeft,
		Width:       12,
		MaxWidth:    8,
		HasMaxWidth: true,
	})
	if left != "abcde...    " {
		t.Fatalf("left-aligned field = %q, want truncated and padded value", left)
	}

	right := alignSessionFormatPart(display.Part{
		Value:       "abcdefghijklmnop",
		Alignment:   display.AlignmentRight,
		Width:       12,
		MaxWidth:    8,
		HasMaxWidth: true,
	})
	if right != "    ...lmnop" {
		t.Fatalf("right-aligned field = %q, want padded tail", right)
	}
}

func TestNarrowRowsDropFieldPaddingBeforeContent(t *testing.T) {
	parts := []display.Part{{
		Field:     display.FieldSessionName,
		Value:     "dev",
		Alignment: display.AlignmentLeft,
		Width:     20,
	}}

	if got := fitSessionFormatParts(parts, 8); got != "dev     " {
		t.Fatalf("fitSessionFormatParts() = %q, want content with reduced padding", got)
	}
}

func TestAgentsDisplayFormatsControlParentAndChildRows(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentSessions.StatusStyle = "text"
	cfg.Display.AgentsSessionFormat = "SESSION {session_name} [{active_window_name}] [{agent_status}]"
	cfg.Display.AgentsAgentFormat = "{tree}{status} {harness_raw}:{agent_session} [{agent_project_name}] {registry_id} {pane_id} {active_marker}{stale}{right}{session_name}"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents
	model.agentSummaryErr = errSaveFailed
	model.filtered = []session.Session{{
		Name:             "project",
		ActivePaneID:     "%7",
		ActiveWindowName: "agents",
		AgentTotal:       1,
		AgentActive:      1,
		Agents: []session.AgentSession{{
			RegistryID:     "registry-1",
			Harness:        "CODEX",
			HarnessSession: "agent-1",
			PaneID:         "%7",
			ProjectRoot:    "/work/project",
			Status:         session.AgentStatusRunning,
		}},
	}}

	lines := strings.Split(stripANSI(model.renderList(88, 2)), "\n")
	if !strings.Contains(lines[0], "SESSION project [agents] [WORKING]") {
		t.Fatalf("custom parent row = %q", lines[0])
	}

	for _, want := range []string{"└─ WORKING", "CODEX:agent-1", "[project]", "registry-1", "%7", "* "} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("custom child row = %q, want %q", lines[1], want)
		}
	}

	if !strings.HasSuffix(lines[1], "project") {
		t.Fatalf("custom child right column = %q, want owning session at right edge", lines[1])
	}
}

func TestAgentTreeRemainsMutedWhenSelected(t *testing.T) {
	model := newTestModel()
	styles := model.sessionRowStyles(sessionTableLayout{})

	for _, last := range []bool{false, true} {
		values := sessionFormatValues{model: model, hasAgent: true, isLastAgent: last}
		tree := values.ResolveSessionFormatField(display.FieldTree)

		for _, selected := range []bool{false, true} {
			var out strings.Builder

			model.writeAgentText(&out, tree+"agent", selected, styles)

			wantStyle := ansiDefaultMuted
			if selected {
				wantStyle = ansiDefaultSelected
			}

			want := wantStyle + "\x1b[22m" + ansiDefaultMuted + agentTreeConnector(last) + "\x1b[m" + wantStyle + "agent" + ansiReset
			if got := out.String(); got != want {
				t.Fatalf("last=%t selected=%t: rendered row = %q, want %q", last, selected, got, want)
			}
		}
	}
}

func TestAgentCountPlaceholderRespectsPopupCounts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentSessions.PopupCounts = false
	cfg.Display.SeshFormat = "{session_name}{right}{agent_count}|{agent_working}/{agent_total}"
	model := newTestModelWithConfig(t, cfg)
	model.filtered = []session.Session{{
		Name:        "project",
		AgentTotal:  2,
		AgentActive: 1,
	}}

	line := stripANSI(model.renderList(48, 1))
	if !strings.HasSuffix(line, "|1/2") || strings.Contains(line, "1/2|1/2") {
		t.Fatalf("popup_counts-disabled row = %q", line)
	}
}

func TestSessionFormatValuesResolveAllDocumentedFields(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	model := newTestModel()
	model.config.AgentSessions.StatusStyle = "text"
	model.displayMode = displayModeAgents
	model.agentSummaryErr = errSaveFailed
	agent := session.AgentSession{
		RegistryID:       "registry-1",
		Harness:          "CODEX",
		HarnessSession:   "agent-1",
		PaneID:           "%7",
		CWD:              "/work/project/pkg",
		ProjectRoot:      "/work/project",
		GitBranch:        "feature/popup",
		Worktree:         "/work/popup-worktree",
		GitChanges:       "+12 -3",
		LinkedWorktree:   true,
		GitAhead:         2,
		GitBehind:        1,
		GitTrackingKnown: true,
		SessionPath:      "/state/codex/session.jsonl",
		Executable:       "/usr/local/bin/codex",
		PID:              412,
		Status:           session.AgentStatusWaiting,
		StatusSince:      now.Add(-2 * time.Minute),
	}
	row := session.Session{
		ID:     "$1",
		Name:   "dev",
		Kind:   session.KindLive,
		Origin: session.OriginSSH,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID:    "work",
			ConnectionLabel: "Work Cluster",
		},
		RemoteStatus:      session.RemoteStatusStale,
		WindowCount:       4,
		Attached:          true,
		Activity:          now.Add(-5 * time.Minute),
		IsCurrent:         true,
		ActivePaneID:      "%7",
		ActiveWindowName:  "editor",
		ActiveWindowIndex: "2",
		ActivePanePath:    "/work/project/pkg",
		ActivePaneCommand: "nvim",
		ActivePaneTitle:   "API implementation",
		AgentTotal:        4,
		AgentActive:       1,
		AgentWaiting:      1,
		AgentUnknown:      1,
		AgentWaitingSince: now.Add(-3 * time.Minute),
		Agents:            []session.AgentSession{agent},
	}
	values := sessionFormatValues{
		model:        model,
		row:          row,
		agent:        agent,
		hasAgent:     true,
		isLastAgent:  true,
		isActivePane: true,
		ageWidth:     2,
		contextWidth: 40,
		now:          now,
	}

	tests := []struct {
		field string
		want  string
	}{
		{field: display.FieldSessionName, want: "dev"},
		{field: display.FieldSessionID, want: "$1"},
		{field: display.FieldSessionKind, want: "live"},
		{field: display.FieldSource, want: "ssh"},
		{field: display.FieldConnection, want: "Work Cluster"},
		{field: display.FieldSourcePrefix, want: "Work Cluster[stale]:"},
		{field: display.FieldSourceStatus, want: "stale"},
		{field: display.FieldWindowCount, want: "4"},
		{field: display.FieldActivityAge, want: "5m"},
		{field: display.FieldAttached, want: "attached"},
		{field: display.FieldCurrent, want: "current"},
		{field: display.FieldCurrentMarker, want: "* "},
		{field: display.FieldAttachedMarker, want: "@ "},
		{field: display.FieldActiveWindowName, want: "editor"},
		{field: display.FieldActiveWindowIndex, want: "2"},
		{field: display.FieldActivePanePath, want: "/work/project/pkg"},
		{field: display.FieldActivePanePathName, want: "pkg"},
		{field: display.FieldActivePaneCommand, want: "nvim"},
		{field: display.FieldActivePaneTitle, want: "API implementation"},
		{field: display.FieldAgentCount, want: "1/4"},
		{field: display.FieldAgentTotal, want: "4"},
		{field: display.FieldAgentWorking, want: "1"},
		{field: display.FieldAgentWaiting, want: "1"},
		{field: display.FieldAgentIdle, want: "1"},
		{field: display.FieldAgentUnknown, want: "1"},
		{field: display.FieldAgentFailed, want: "0"},
		{field: display.FieldAgentInterrupted, want: "0"},
		{field: display.FieldAgentStatus, want: "WAITING"},
		{field: display.FieldAgentStatusText, want: "WAITING"},
		{field: display.FieldAgentStatusGlyph, want: "\x1b[38;2;255;221;87m●\x1b[m"},
		{field: display.FieldAgentAge, want: "3m"},
		{field: display.FieldAgentDuration, want: "00:03:00"},
		{field: display.FieldAgentClock, want: "00:03:00"},
		{field: display.FieldSessionAgentContext, want: ""},
		{field: display.FieldTree, want: "\x1b[22m\x1b[38;2;108;112;134m└─ \x1b[m"},
		{field: display.FieldTreeBranch, want: "\x1b[22m\x1b[38;2;108;112;134m└─ \x1b[m"},
		{field: display.FieldTreeIndent, want: "   "},
		{field: display.FieldShortcut, want: ""},
		{field: display.FieldShortcutBadge, want: ""},
		{field: display.FieldHarness, want: "Codex"},
		{field: display.FieldHarnessRaw, want: "CODEX"},
		{field: display.FieldAgentSession, want: "agent-1"},
		{field: display.FieldRegistryID, want: "registry-1"},
		{field: display.FieldPaneID, want: "%7"},
		{field: display.FieldStatus, want: "WAITING"},
		{field: display.FieldStatusText, want: "WAITING"},
		{field: display.FieldStatusGlyph, want: "\x1b[38;2;255;221;87m●\x1b[m"},
		{field: display.FieldStatusAge, want: "2m"},
		{field: display.FieldStatusDuration, want: "00:02:00"},
		{field: display.FieldStatusClock, want: "00:02:00"},
		{field: display.FieldActiveMarker, want: "* "},
		{field: display.FieldStale, want: ""},
		{field: display.FieldAgentCWD, want: "/work/project/pkg"},
		{field: display.FieldAgentCWDName, want: "pkg"},
		{field: display.FieldAgentProjectRoot, want: "/work/project"},
		{field: display.FieldAgentProjectName, want: "project"},
		{field: display.FieldAgentGitBranch, want: "feature/popup"},
		{field: display.FieldAgentGitGlyph, want: ""},
		{field: display.FieldAgentGitChanges, want: "+12 -3"},
		{field: display.FieldAgentGitInfo, want: "feature/popup / popup-worktree +12 -3"},
		{field: display.FieldAgentGitLabel, want: "feature/popup / popup-worktree"},
		{field: display.FieldAgentGitAdded, want: "\x1b[2;38;2;80;250;123m+12\x1b[m"},
		{field: display.FieldAgentGitDeleted, want: "\x1b[2;38;2;255;77;166m-3\x1b[m"},
		{field: display.FieldAgentGitAhead, want: "\x1b[38;2;36;200;255m↑2\x1b[m"},
		{field: display.FieldAgentGitBehind, want: "\x1b[38;2;255;221;87m↓1\x1b[m"},
		{field: display.FieldAgentWorktree, want: "/work/popup-worktree"},
		{field: display.FieldAgentWorktreeName, want: "popup-worktree"},
		{field: display.FieldAgentSessionPath, want: "/state/codex/session.jsonl"},
		{field: display.FieldAgentSessionFile, want: "session.jsonl"},
		{field: display.FieldAgentExecutable, want: "/usr/local/bin/codex"},
		{field: display.FieldAgentPID, want: "412"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			if got := values.ResolveSessionFormatField(tt.field); got != tt.want {
				t.Fatalf("ResolveSessionFormatField() = %q, want %q", got, tt.want)
			}
		})
	}

	context := values.ResolveSessionFormatField(display.FieldAgentContext)
	for _, want := range []string{"* WAITING", "2m"} {
		if !strings.Contains(context, want) {
			t.Fatalf("agent_context = %q, want %q", context, want)
		}
	}
}

func TestShortcutFieldResolution(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.width = 80
	model.height = 10
	model.filtered = []session.Session{
		{
			Name: "dev",
			Agents: []session.AgentSession{
				{Harness: "pi", PaneID: "%1"},
			},
		},
	}
	model.cursor = 0
	model.agentCursor = 0

	values := sessionFormatValues{
		model:        model,
		row:          model.filtered[0],
		agent:        model.filtered[0].Agents[0],
		hasAgent:     true,
		isLastAgent:  true,
		isActivePane: true,
	}

	if got := values.ResolveSessionFormatField(display.FieldShortcut); got != "\x1b[38;2;108;112;134m1\x1b[m" {
		t.Fatalf("FieldShortcut = %q, want muted 1", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldShortcutBadge); got != "\x1b[38;2;108;112;134m^1\x1b[m" {
		t.Fatalf("FieldShortcutBadge = %q, want muted ^1", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldTreeIndent); got != "\x1b[22m\x1b[38;2;108;112;134m 1 \x1b[m" {
		t.Fatalf("FieldTreeIndent = %q, want muted ' 1 '", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldTreeBranch); got != "\x1b[22m\x1b[38;2;108;112;134m└─ \x1b[m" {
		t.Fatalf("FieldTreeBranch = %q, want tree branch", got)
	}
}

func TestAgentGitCountersKeepSeparateColumns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.AgentsAgentFormat = "{agent_session}{right} {agent_git_label:<6.12}|{agent_git_added:<5}|{agent_git_deleted:<5}|{agent_git_ahead:<2}|{agent_git_behind:<2}"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{Name: "dev", AgentTotal: 2, Agents: []session.AgentSession{
		{HarnessSession: "first", Worktree: "/work/repo", GitBranch: "main", GitChanges: "+1 -200", GitAhead: 2, GitBehind: 12, GitTrackingKnown: true},
		{HarnessSession: "second", Worktree: "/work/repo", GitBranch: "feature/long-branch-name", GitChanges: "+1234 -0", GitTrackingKnown: true},
	}}}
	lines := strings.Split(stripANSI(model.renderList(100, 3)), "\n")

	var columns []int

	for rowIndex, line := range lines[1:] {
		var actual []int

		for column, char := range []rune(line) {
			if char == '|' || char == '' {
				actual = append(actual, column)
			}
		}

		if len(actual) != 5 {
			t.Fatalf("missing Git columns in %q", line)
		}

		if rowIndex == 0 {
			columns = actual
			continue
		}

		for index := range columns {
			if columns[index] != actual[index] {
				t.Fatalf("Git column %d shifted from %d to %d", index, columns[index], actual[index])
			}
		}
	}

	if !strings.Contains(lines[1], "↑2") || !strings.Contains(lines[1], "↓12") || !strings.Contains(lines[2], "↑0") {
		t.Fatalf("tracking counters missing: %#v", lines)
	}
}

func TestAgentOutsideGitHidesBranchIconAndGitColumns(t *testing.T) {
	cfg := config.DefaultConfig()
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents

	model.filtered = []session.Session{{
		Name:       "Projects",
		AgentTotal: 1,
		Agents: []session.AgentSession{
			{
				PaneID:         "%1",
				HarnessSession: "Find tmux bug and PR fix session",
				Harness:        "Omp",
				CWD:            "/home/user/Projects",
			},
		},
	}}

	output := stripANSI(model.renderList(100, 3))
	if strings.Contains(output, "") {
		t.Fatalf("agent outside Git rendered branch icon: %q", output)
	}

	model.filtered = []session.Session{{
		Name:       "Mixed",
		AgentTotal: 2,
		Agents: []session.AgentSession{
			{
				PaneID:         "%1",
				HarnessSession: "git agent",
				Worktree:       "/work/repo",
				GitBranch:      "main",
				GitChanges:     "+5 -2",
			},
			{
				PaneID:         "%2",
				HarnessSession: "no-git agent",
				CWD:            "/home/user/Projects",
			},
		},
	}}

	lines := strings.Split(stripANSI(model.renderList(100, 4)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %#v", lines)
	}

	if !strings.Contains(lines[1], "") || !strings.Contains(lines[1], "main") {
		t.Fatalf("git agent row missing git info: %q", lines[1])
	}

	if strings.Contains(lines[2], "") {
		t.Fatalf("no-git agent row showed git branch icon: %q", lines[2])
	}

	cfgRight := config.DefaultConfig()
	cfgRight.Settings.ShortcutStyle = "right"
	modelRight := newTestModelWithConfig(t, cfgRight)
	modelRight.displayMode = displayModeAgents
	modelRight.filtered = []session.Session{{
		Name:       "Projects",
		AgentTotal: 1,
		Agents: []session.AgentSession{
			{
				PaneID:         "%1",
				HarnessSession: "task",
				CWD:            "/home/user/Projects",
			},
		},
	}}

	outputRight := stripANSI(modelRight.renderList(100, 3))
	if strings.Contains(outputRight, "") {
		t.Fatalf("no-git agent with right shortcut showed git icon: %q", outputRight)
	}

	if !strings.Contains(outputRight, "1") {
		t.Fatalf("no-git agent with right shortcut missing shortcut badge: %q", outputRight)
	}
}

func TestAgentSessionExpandsWhenSpacePermits(t *testing.T) {
	cfg := config.DefaultConfig()
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents

	longTitle := "Fix git branch icon in non-git folder without git repo"
	model.filtered = []session.Session{{
		Name:       "sesh",
		AgentTotal: 2,
		Agents: []session.AgentSession{
			{
				PaneID:         "%1",
				HarnessSession: longTitle,
				Harness:        "Omp",
				Worktree:       "/work/sesh",
				GitBranch:      "master",
				GitChanges:     "+293 -16",
			},
			{
				PaneID:         "%2",
				HarnessSession: "no-git agent with long session title to verify layout",
				Harness:        "Omp",
				CWD:            "/home/user/Projects",
			},
		},
	}}

	wide := stripANSI(model.renderList(140, 4))

	lines := strings.Split(wide, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %#v", lines)
	}

	if !strings.Contains(lines[1], longTitle) {
		t.Fatalf("wide render truncated session name: %q", lines[1])
	}

	if !strings.Contains(lines[1], "Omp      master") {
		t.Fatalf("harness not placed directly on left of branch: %q", lines[1])
	}

	idx1 := strings.Index(lines[1], "Omp")
	idx2 := strings.Index(lines[2], "Omp")

	if idx1 < 0 || idx2 < 0 || idx1 != idx2 {
		t.Fatalf("harness columns misaligned: row 1 at %d, row 2 at %d:\n%s", idx1, idx2, wide)
	}

	narrow := stripANSI(model.renderList(70, 4))
	if strings.Contains(narrow, longTitle) {
		t.Fatalf("narrow render should have truncated session name: %q", narrow)
	}

	if !strings.Contains(narrow, "...") {
		t.Fatalf("narrow render missing ellipsis: %q", narrow)
	}
}

func TestAgentGitGlyphEmptyOutsideGit(t *testing.T) {
	values := sessionFormatValues{
		model: newTestModel(),
		agent: session.AgentSession{CWD: "/home/user/Projects"},
	}

	if got := values.ResolveSessionFormatField(display.FieldAgentGitGlyph); got != "" {
		t.Fatalf("FieldAgentGitGlyph outside git = %q, want empty", got)
	}
}

func TestAgentGitInfoOptionalMetadata(t *testing.T) {
	for _, tt := range []struct {
		name  string
		agent session.AgentSession
		want  string
	}{
		{name: "outside Git", want: ""},
		{name: "detached", agent: session.AgentSession{Worktree: "/work/repo", GitChanges: "+1 -2"}, want: "detached +1 -2"},
		{name: "main checkout", agent: session.AgentSession{Worktree: "/work/repo", GitBranch: "main", GitChanges: "+0 -0"}, want: "main +0 -0"},
		{name: "unavailable diff", agent: session.AgentSession{Worktree: "/work/repo", GitBranch: "main"}, want: "main"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentGitInfo(tt.agent); got != tt.want {
				t.Fatalf("agentGitInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalSessionSourceFormatFieldsPreserveUnprefixedNames(t *testing.T) {
	values := sessionFormatValues{
		model: newTestModel(),
		row:   session.Session{Name: "dev"},
	}

	if got := values.ResolveSessionFormatField(display.FieldSource); got != "local" {
		t.Fatalf("source = %q, want local", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldConnection); got != "" {
		t.Fatalf("connection = %q, want empty", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldSourcePrefix); got != "" {
		t.Fatalf("source_prefix = %q, want empty", got)
	}
}

func TestSessionMarkersPreserveAlignmentWhenInactive(t *testing.T) {
	values := sessionFormatValues{model: newTestModel()}

	if current := values.ResolveSessionFormatField(display.FieldCurrentMarker); current != "  " {
		t.Fatalf("current_marker = %q, want alignment spaces", current)
	}

	if attached := values.ResolveSessionFormatField(display.FieldAttachedMarker); attached != "  " {
		t.Fatalf("attached_marker = %q, want alignment spaces", attached)
	}
}

func TestModelRejectsInvalidDirectDisplayConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.SeshFormat = "{not_a_field}"

	_, err := newModel(cfg, &session.State{})
	if err == nil || !strings.Contains(err.Error(), "display.sesh_format") {
		t.Fatalf("newModel() error = %v, want display format context", err)
	}
}

func TestSessionFormatStatusTimeFormatClock(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cfg := config.DefaultConfig()
	cfg.AgentSessions.StatusTimeFormat = "clock"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents

	agent := session.AgentSession{
		Status:      session.AgentStatusRunning,
		StatusSince: now.Add(-(time.Minute + 23*time.Second)),
	}
	row := session.Session{
		AgentTotal:        1,
		AgentWaiting:      1,
		AgentWaitingSince: now.Add(-(5*time.Minute + 42*time.Second)),
	}
	values := sessionFormatValues{
		model:    model,
		row:      row,
		agent:    agent,
		hasAgent: true,
		now:      now,
	}

	if got := values.ResolveSessionFormatField(display.FieldStatusAge); got != "00:01:23" {
		t.Fatalf("status_age in clock format = %q, want 00:01:23", got)
	}

	if got := values.ResolveSessionFormatField(display.FieldAgentAge); got != "00:05:42" {
		t.Fatalf("agent_age in clock format = %q, want 00:05:42", got)
	}
}
