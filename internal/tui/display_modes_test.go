package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestDisplayModeRestoresPersistedAgentsMode(t *testing.T) {
	model, err := newModelWithDeps(config.DefaultConfig(), &session.State{
		PopupDisplayMode: "agents",
	}, Dependencies{})
	if err != nil {
		t.Fatalf("newModelWithDeps() error = %v", err)
	}

	if model.displayMode != displayModeAgents {
		t.Fatalf("displayMode = %q, want agents", model.displayMode)
	}
}

func TestDisplayModeUnknownPersistedValueFallsBackToSesh(t *testing.T) {
	model, err := newModelWithDeps(config.DefaultConfig(), &session.State{
		PopupDisplayMode: "future-mode",
	}, Dependencies{})
	if err != nil {
		t.Fatalf("newModelWithDeps() error = %v", err)
	}

	if model.displayMode != displayModeSesh {
		t.Fatalf("displayMode = %q, want sesh", model.displayMode)
	}
}

func TestDisplayModeParsingPreservesConfiguredOrderAndPersistedFallback(t *testing.T) {
	for _, tt := range []struct {
		persisted string
		want      displayMode
	}{
		{persisted: "future-mode", want: displayModeSesh},
		{persisted: " AGENTS ", want: displayModeAgents},
		{persisted: "agentſ", want: displayModeAgents},
	} {
		t.Run(tt.persisted, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Settings.DisplayModes = []string{"future-mode", "agentſ", " AGENTS ", "SeSh"}

			model, err := newModelWithDeps(cfg, &session.State{
				PopupDisplayMode: tt.persisted,
			}, Dependencies{})
			if err != nil {
				t.Fatalf("newModelWithDeps() error = %v", err)
			}

			if model.displayMode != tt.want {
				t.Fatalf("persisted display mode = %q, want %q", model.displayMode, tt.want)
			}

			if len(model.displayModes) != 2 || model.displayModes[0] != displayModeAgents || model.displayModes[1] != displayModeSesh {
				t.Fatalf("configured display modes = %v, want agents followed by sesh", model.displayModes)
			}
		})
	}
}

func TestSingleConfiguredDisplayModeOverridesPersistedMode(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		persisted  string
		want       displayMode
	}{
		{name: "SESH only", configured: "sesh", persisted: "agents", want: displayModeSesh},
		{name: "AGENTS only", configured: "agents", persisted: "sesh", want: displayModeAgents},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Settings.DisplayModes = []string{tt.configured}

			model, err := newModelWithDeps(cfg, &session.State{
				PopupDisplayMode: tt.persisted,
			}, Dependencies{})
			if err != nil {
				t.Fatalf("newModelWithDeps() error = %v", err)
			}

			if model.displayMode != tt.want || model.showsDisplayModeIndicator() {
				t.Fatalf("display mode/indicator = %q/%v, want %q/false", model.displayMode, model.showsDisplayModeIndicator(), tt.want)
			}
		})
	}
}

func TestSingleDisplayModeMakesCycleKeyNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Settings.DisplayModes = []string{"agents"}
	saves := 0

	model, err := newModelWithDeps(cfg, &session.State{}, Dependencies{
		SaveModelState: func(*session.State) error {
			saves++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newModelWithDeps() error = %v", err)
	}

	model = updateTestModelWithCommand(t, model, keyCtrl('f'))
	if model.displayMode != displayModeAgents || saves != 0 {
		t.Fatalf("cycle result = %q with %d saves, want locked agents with no save", model.displayMode, saves)
	}

	model.width = 60
	model.height = 12

	output := stripANSI(model.renderFrame())
	if strings.Contains(output, "AGENTS") {
		t.Fatalf("single-mode frame contains mode indicator: %q", output)
	}

	if strings.Contains(stripANSI(model.renderHelpOverlay()), "Cycle display mode") {
		t.Fatal("single-mode help contains cycle action")
	}

	model.config.Settings.ShowFooter = false
	model.config.AgentSessions.Enabled = false

	if layout := model.calcPopupLayout(false); layout.showFooter {
		t.Fatal("single-mode popup reserved a footer with hints disabled")
	}
}

func TestSingleSeshModeMakesCycleKeyNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Settings.DisplayModes = []string{"sesh"}
	saves := 0

	model, err := newModelWithDeps(cfg, &session.State{
		PopupDisplayMode: "agents",
	}, Dependencies{
		SaveModelState: func(*session.State) error {
			saves++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	model = updateTestModelWithCommand(t, model, keyCtrl('f'))
	if model.displayMode != displayModeSesh || saves != 0 || model.showsDisplayModeIndicator() {
		t.Fatalf(
			"single SESH cycle result = %q with %d saves and indicator %v",
			model.displayMode,
			saves,
			model.showsDisplayModeIndicator(),
		)
	}
}

func TestCycleDisplayModeFiltersAgentsWrapsAndPersists(t *testing.T) {
	savedModes := make([]string, 0, 2)

	model, err := newModelWithDeps(config.DefaultConfig(), &session.State{}, Dependencies{
		SaveModelState: func(st *session.State) error {
			savedModes = append(savedModes, st.PopupDisplayMode)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newModelWithDeps() error = %v", err)
	}

	model.sessions = []session.Session{
		{ID: "$1", Name: "plain", Kind: session.KindLive},
		{ID: "$2", Name: "agent", Kind: session.KindLive, AgentTotal: 1, Agents: []session.AgentSession{{RegistryID: "agent-1"}}},
		{Name: "saved-agent", Kind: session.KindSaved, AgentTotal: 1},
	}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.cursor = 1

	model = updateTestModelWithCommand(t, model, keyCtrl('f'))
	assertDisplayModeState(t, model, displayModeAgents, "agents")

	if got := sessionNamesForTest(model.filtered); !slicesEqual(got, []string{"agent", "plain"}) {
		t.Fatalf("agent rows = %#v, want agent and regular live sessions", got)
	}

	selection := struct {
		cursor int
		id     string
	}{model.cursor, model.filtered[model.cursor].ID}
	if want := (struct {
		cursor int
		id     string
	}{0, "$2"}); selection != want {
		t.Fatalf("selection = %#v, want %#v", selection, want)
	}

	model = updateTestModelWithCommand(t, model, keyCtrl('f'))
	assertDisplayModeState(t, model, displayModeSesh, "sesh")

	wrapped := struct {
		rowCount int
		id       string
	}{len(model.filtered), model.filtered[model.cursor].ID}
	if want := (struct {
		rowCount int
		id       string
	}{3, "$2"}); wrapped != want {
		t.Fatalf("wrapped rows/selection = %#v, want %#v", wrapped, want)
	}

	if got := strings.Join(savedModes, ","); got != "agents,sesh" {
		t.Fatalf("saved modes = %q, want agents,sesh", got)
	}
}

func TestCycleDisplayModeSelectsCurrentSessionActiveAgent(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{ID: "$1", Name: "other", Kind: session.KindLive},
		{
			ID:           "$2",
			Name:         "current",
			Kind:         session.KindLive,
			IsCurrent:    true,
			ActivePaneID: "%8",
			Agents: []session.AgentSession{
				{RegistryID: "agent-1", PaneID: "%7"},
				{RegistryID: "agent-2", PaneID: "%8"},
			},
		},
	}
	model.refreshDisplaySessions()
	model.filterSessions()

	model = updateTestModel(t, model, keyCtrl('f'))

	if model.filtered[model.cursor].ID != "$2" || model.agentCursor != 1 {
		t.Fatalf(
			"AGENTS selection = session:%q agent:%d, want current session $2 active agent 1",
			model.filtered[model.cursor].ID,
			model.agentCursor,
		)
	}
}

func TestCycleDisplayModeRedrawsPopupInPlaceAndPreservesSearch(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.textInput.SetValue("project agent")
	model.textInput.CursorEnd()
	dependenciesForTest(model).SaveModelState = func(_ *session.State) error { return nil }

	handler := model.handleActionKeys(keyCtrl('f'))
	if handler == nil {
		t.Fatal("cycle display mode returned nil handler")
	}

	updated, cmd := handler()
	if updated != model || cmd == nil {
		t.Fatalf("cycle result = model:%T cmd:%v, want same model with persistence command", updated, cmd != nil)
	}

	model = updateTestModel(t, model, cmd())

	if model.displayMode != displayModeAgents || model.textInput.Value() != "project agent" {
		t.Fatalf("cycle state = mode:%q query:%q, want agents with preserved search", model.displayMode, model.textInput.Value())
	}
}

func TestCycleDisplayModeStaysOpenWhenPersistenceFails(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	dependenciesForTest(model).SaveModelState = func(_ *session.State) error { return errSaveFailed }

	handler := model.handleActionKeys(keyCtrl('f'))
	if handler == nil {
		t.Fatal("cycle display mode returned nil handler")
	}

	updated, cmd := handler()
	if updated != model || cmd == nil {
		t.Fatalf(
			"failed persistence cycle = model:%T cmd:%v, want same model with persistence command",
			updated,
			cmd != nil,
		)
	}

	model = updateTestModel(t, model, cmd())
	if model.statusLevel != statusError {
		t.Fatalf("failed persistence status = %v, want error", model.statusLevel)
	}
}

func assertDisplayModeState(t *testing.T, model *Model, wantMode displayMode, wantPersisted string) {
	t.Helper()

	got := struct {
		mode      displayMode
		persisted string
	}{model.displayMode, model.state.PopupDisplayMode}

	want := struct {
		mode      displayMode
		persisted string
	}{wantMode, wantPersisted}
	if got != want {
		t.Fatalf("display mode/state = %#v, want %#v", got, want)
	}
}

func TestAgentsModePreservesSearchAndSortsSessionsByName(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{ID: "$1", Name: "charlie", AgentTotal: 1, AgentIdleSince: now.Add(-2 * time.Minute), Agents: []session.AgentSession{{RegistryID: "charlie"}}},
		{ID: "$2", Name: "alpha", AgentTotal: 2, AgentActive: 1, AgentActivityChangedAt: now.Add(-time.Minute), Agents: []session.AgentSession{{RegistryID: "alpha-1"}, {RegistryID: "alpha-2"}}},
		{ID: "$3", Name: "bravo", AgentTotal: 1, AgentWaiting: 1, AgentWaitingSince: now.Add(-10 * time.Minute), Agents: []session.AgentSession{{RegistryID: "bravo"}}},
		{ID: "$4", Name: "plain"},
	}
	model.refreshDisplaySessions()
	model.filterSessions()

	want := []string{"alpha", "bravo", "charlie", "plain"}
	if got := sessionNamesForTest(model.filtered); !slicesEqual(got, want) {
		t.Fatalf("agent rows = %#v, want %#v", got, want)
	}

	model.textInput.SetValue("a")
	model.filterSessions()

	if model.textInput.Value() != "a" {
		t.Fatalf("search = %q, want preserved a", model.textInput.Value())
	}

	if len(model.filtered) != 4 {
		t.Fatalf("searched agent rows = %#v, want four matches", model.filtered)
	}
}

func TestAgentSummaryRefreshKeepsSessionOrderAndSelectionStable(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{ID: "$1", Name: "alpha", AgentTotal: 1, AgentActive: 1, Agents: []session.AgentSession{{RegistryID: "alpha", Status: session.AgentStatusRunning}}},
		{ID: "$2", Name: "bravo", AgentTotal: 1, AgentActive: 1, Agents: []session.AgentSession{{RegistryID: "bravo", Status: session.AgentStatusRunning}}},
		{ID: "$3", Name: "charlie", AgentTotal: 1, AgentActive: 1, Agents: []session.AgentSession{{RegistryID: "charlie", Status: session.AgentStatusRunning}}},
	}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.cursor = 1
	model.agentCursor = 0

	model.applyAgentSummaries([]session.AgentSummary{
		{TmuxSessionID: "$1", TmuxSessionName: "alpha", Total: 1, Agents: []session.AgentSession{{RegistryID: "alpha", Status: session.AgentStatusIdle}}},
		{TmuxSessionID: "$2", TmuxSessionName: "bravo", Total: 1, Waiting: 1, Agents: []session.AgentSession{{RegistryID: "bravo", Status: session.AgentStatusWaiting}}},
		{TmuxSessionID: "$3", TmuxSessionName: "charlie", Total: 1, Active: 1, Agents: []session.AgentSession{{RegistryID: "charlie", Status: session.AgentStatusRunning}}},
	})

	want := []string{"alpha", "bravo", "charlie"}
	if got := sessionNamesForTest(model.filtered); !slicesEqual(got, want) {
		t.Fatalf("agent rows after status refresh = %#v, want %#v", got, want)
	}

	if model.cursor != 1 || model.agentCursor != 0 || model.filtered[model.cursor].Name != "bravo" {
		t.Fatalf(
			"selection after status refresh = %d/%d on %q, want 1/0 on bravo",
			model.cursor,
			model.agentCursor,
			model.filtered[model.cursor].Name,
		)
	}
}

func TestAgentContextLabelsStateAndIdleAge(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	model := newTestModel()
	model.displayMode = displayModeAgents

	tests := []struct {
		name      string
		row       session.Session
		wantGlyph string
		wantText  string
	}{
		{name: "working", row: session.Session{AgentActive: 2, AgentTotal: 3}, wantGlyph: "●", wantText: "WORKING"},
		{name: "waiting", row: session.Session{AgentWaiting: 1, AgentTotal: 2, AgentWaitingSince: now.Add(-4 * time.Minute)}, wantGlyph: "● 4m", wantText: "WAITING 4m"},
		{name: "failed", row: session.Session{AgentFailed: 1, AgentTotal: 2}, wantGlyph: "●", wantText: "FAILED"},
		{name: "interrupted", row: session.Session{AgentInterrupted: 1, AgentTotal: 2}, wantGlyph: "●", wantText: "PAUSED"},
		{name: "idle", row: session.Session{AgentTotal: 1, AgentIdleSince: now.Add(-18 * time.Minute)}, wantGlyph: "○ 18m", wantText: "IDLE 18m"},
		{name: "unknown", row: session.Session{AgentTotal: 1, AgentUnknown: 1}, wantGlyph: "◌", wantText: "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run("glyph_"+tt.name, func(t *testing.T) {
			if got := stripANSI(model.formattedAgentContextAt(tt.row, now, 0)); got != tt.wantGlyph {
				t.Fatalf("formattedAgentContextAt() = %q, want %q", got, tt.wantGlyph)
			}
		})
	}

	if got := model.agentContextLabelAt(session.Session{AgentActive: 1, AgentTotal: 1}); got != "" {
		t.Fatalf("agentContextLabelAt in agents mode = %q, want empty (no circle on session row)", got)
	}

	model.config.AgentSessions.StatusStyle = "text"

	for _, tt := range tests {
		t.Run("text_"+tt.name, func(t *testing.T) {
			if got := strings.TrimSpace(model.formattedAgentContextAt(tt.row, now, 0)); got != tt.wantText {
				t.Fatalf("formattedAgentContextAt() = %q, want %q", got, tt.wantText)
			}
		})
	}
}

func TestFormattedAgentContextAlignsStateAndAgeColumns(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	shortAge := formattedAgentContextAt(session.Session{
		AgentTotal:     1,
		AgentIdleSince: now.Add(-3 * time.Minute),
	}, now, 3)
	longAge := formattedAgentContextAt(session.Session{
		AgentTotal:     12,
		AgentIdleSince: now.Add(-20 * time.Minute),
	}, now, 3)
	working := formattedAgentContextAt(session.Session{
		AgentActive: 1,
		AgentTotal:  2,
	}, now, 3)

	statusColumn := strings.Index(shortAge, "IDLE")
	if strings.Index(longAge, "IDLE") != statusColumn || strings.Index(working, "WORKING") != statusColumn {
		t.Fatalf("contexts are not state-aligned: %q / %q / %q", shortAge, longAge, working)
	}

	if !strings.HasSuffix(shortAge, " 3m") || !strings.HasSuffix(longAge, "20m") {
		t.Fatalf("idle ages are not right-aligned: %q / %q", shortAge, longAge)
	}
}

func TestFormattedAgentSessionContextAlignsMinuteAndHourAges(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	workingAge := "1h 23m"
	ageWidth := len(workingAge)
	working := formattedAgentSessionContextAt(session.AgentSession{
		Status:      session.AgentStatusRunning,
		StatusSince: now.Add(-(time.Hour + 23*time.Minute)),
	}, now, ageWidth, false)
	idle := formattedAgentSessionContextAt(session.AgentSession{
		Status:      session.AgentStatusIdle,
		StatusSince: now.Add(-7 * time.Minute),
	}, now, ageWidth, false)

	if !strings.HasSuffix(working, workingAge) || !strings.HasSuffix(idle, "    7m") {
		t.Fatalf("agent ages are not right-aligned: %q / %q", working, idle)
	}

	if strings.Index(working, "WORKING") != strings.Index(idle, "IDLE") {
		t.Fatalf("agent statuses are not aligned: %q / %q", working, idle)
	}
}

func TestFormattedAgentSessionContextClockFormatAligns(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cfg := config.DefaultConfig()
	cfg.AgentSessions.StatusTimeFormat = "clock"
	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeAgents

	working := model.formattedAgentSessionContextAt(session.AgentSession{
		Status:      session.AgentStatusRunning,
		StatusSince: now.Add(-(time.Hour + 23*time.Minute + 45*time.Second)),
	}, now, 8, false, false)
	idle := model.formattedAgentSessionContextAt(session.AgentSession{
		Status:      session.AgentStatusIdle,
		StatusSince: now.Add(-(time.Minute + 23*time.Second)),
	}, now, 8, false, false)

	if !strings.HasSuffix(working, "01:23:45") {
		t.Fatalf("working age = %q, want suffix 01:23:45", working)
	}

	if !strings.HasSuffix(idle, "00:01:23") {
		t.Fatalf("idle age = %q, want suffix 00:01:23", idle)
	}
}

func TestAgentsModeInitialSelectionFocusesCurrentSessionAndActivePane(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{
			Name: "other-session",
			Agents: []session.AgentSession{
				{RegistryID: "other-agent-1", PaneID: "%1"},
			},
		},
		{
			Name:         "current-session",
			IsCurrent:    true,
			ActivePaneID: "%3",
			Agents: []session.AgentSession{
				{RegistryID: "current-agent-1", PaneID: "%2"},
				{RegistryID: "current-agent-2", PaneID: "%3"},
			},
		},
		{
			Name: "regular-session",
		},
	}

	model.restoreSelectedSession(sessionIdentity{})

	selected := model.selectedListRow()
	if selected.sessionIndex != 1 || selected.agentIndex != 1 {
		t.Fatalf("initial selection = %#v, want session 1 agent 1 (current-session/current-agent-2)", selected)
	}
}

func TestAgentsModeInitialSelectionWhenActiveSessionHasNoAgents(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{
			Name: "agent-session",
			Agents: []session.AgentSession{
				{RegistryID: "agent-1", PaneID: "%1"},
			},
		},
		{
			Name:      "current-regular-session",
			IsCurrent: true,
		},
	}

	model.restoreSelectedSession(sessionIdentity{})

	selected := model.selectedListRow()
	if selected.sessionIndex != 1 || selected.isAgent() {
		t.Fatalf("initial selection = %#v, want regular current session (1, -1)", selected)
	}
}

func TestAgentsModeInitialSelectionWhenNoPaneMatchesActivePaneID(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{
			Name:         "current-session",
			IsCurrent:    true,
			ActivePaneID: "%99",
			Agents: []session.AgentSession{
				{RegistryID: "agent-1", PaneID: "%1"},
				{RegistryID: "agent-2", PaneID: "%2"},
			},
		},
	}

	model.restoreSelectedSession(sessionIdentity{})

	selected := model.selectedListRow()
	if selected.sessionIndex != 0 || selected.agentIndex != 0 {
		t.Fatalf("initial selection = %#v, want first agent (0, 0)", selected)
	}
}

func TestAgentsModeInitialSelectionWhenNoSessionIsCurrent(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{
		{
			Name: "first-session",
			Agents: []session.AgentSession{
				{RegistryID: "agent-1", PaneID: "%1"},
			},
		},
		{
			Name: "second-session",
		},
	}
	model.cursor = 0
	model.agentCursor = 0

	model.restoreSelectedSession(sessionIdentity{})

	selected := model.selectedListRow()
	if selected.sessionIndex != 0 || selected.agentIndex != 0 {
		t.Fatalf("initial selection = %#v, want default (0, 0)", selected)
	}
}
