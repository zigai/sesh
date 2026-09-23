package tui

import (
	"fmt"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/session"
)

func TestFilterSessionsMatchesSavedRows(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{Name: testSupportSessionDevName, Kind: session.KindLive},
		{Name: "archive", Kind: session.KindSaved},
		{Name: "docs", Kind: session.KindSaved},
	}
	model.refreshDisplaySessions()
	model.textInput.SetValue("arc")

	model.filterSessions()

	if len(model.filtered) != 1 {
		t.Fatalf("filtered sessions = %#v, want one saved archive row", model.filtered)
	}

	if got := model.filtered[0]; got.Name != "archive" || got.Kind != session.KindSaved {
		t.Fatalf("filtered row = %#v, want saved archive row", got)
	}
}

func TestClearingSearchAfterWhitespacePreservesDisplaySessions(t *testing.T) {
	for _, mode := range []displayMode{displayModeSesh, displayModeAgents} {
		t.Run(string(mode), func(t *testing.T) {
			model := newTestModel()
			model.displayMode = mode
			model.sessions = []session.Session{
				{Name: "alpha", ID: "$1"},
				{Name: "zebra", ID: "$2"},
				{Name: "omega", ID: "$3"},
			}
			model.refreshDisplaySessions()

			for _, query := range []string{" ", "zebra", ""} {
				model.textInput.SetValue(query)
				model.filterSessions()

				want := []string{"alpha", "zebra", "omega"}
				if query == "zebra" {
					want = []string{"zebra"}
				}

				if got := sessionNamesForTest(model.filtered); !slices.Equal(got, want) {
					t.Fatalf("query %q returned %v, want %v", query, got, want)
				}
			}
		})
	}
}

func TestFilterSessionsMatchesRemoteSourceFields(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{Name: testSupportSessionDevName, Kind: session.KindSaved},
		{
			Name:         testSupportSessionDevName,
			Kind:         session.KindLive,
			Origin:       session.OriginSSH,
			RemoteStatus: session.RemoteStatusDegraded,
			RuntimeIdentity: session.RuntimeIdentity{
				ConnectionID:    "work",
				ConnectionLabel: "Engineering",
			},
		},
	}
	model.refreshDisplaySessions()

	for _, query := range []string{"work", "Engineering", "ssh", "degraded"} {
		model.textInput.SetValue(query)
		model.filterSessions()

		if len(model.filtered) != 1 || model.filtered[0].Origin != session.OriginSSH {
			t.Fatalf("query %q filtered sessions = %#v, want only remote dev", query, model.filtered)
		}
	}
}

func TestFilterSessionsRanksUsefulSessionNameMatches(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{Name: "backend"},
		{Name: "dev-api"},
		{Name: "development_api"},
		{Name: "DevApi"},
		{Name: "archive"},
	}
	model.refreshDisplaySessions()
	model.textInput.SetValue("da")

	model.filterSessions()

	got := sessionNamesForTest(model.filtered)

	want := []string{"DevApi", "dev-api", "development_api"}
	if !slicesEqual(got, want) {
		t.Fatalf("filtered sessions = %#v, want %#v", got, want)
	}
}

func TestFilterSessionsRanksContiguousPrefixAboveShortGap(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{Name: "abge"},
		{Name: "agent-sessions"},
	}
	model.refreshDisplaySessions()
	model.textInput.SetValue("age")

	model.filterSessions()

	got := sessionNamesForTest(model.filtered)

	want := []string{"agent-sessions", "abge"}
	if !slicesEqual(got, want) {
		t.Fatalf("filtered sessions = %#v, want %#v", got, want)
	}
}

func TestAgentFilteringKeepsSelectionOnAgentRow(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{Name: "first", Kind: session.KindLive, Agents: []session.AgentSession{{RegistryID: "agent-1"}}},
		{Name: "second", Kind: session.KindLive, Agents: []session.AgentSession{{RegistryID: "agent-2"}}},
	}
	model.refreshDisplaySessions()
	model.filterSessions()
	model.agentCursor = 0
	model.textInput.SetValue("second")
	model.filterSessions()

	if got := sessionNamesForTest(model.filtered); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("filtered sessions = %#v, want second", got)
	}

	if got, want := model.selectedListRow(), (popupListRow{sessionIndex: 0, agentIndex: 0}); !got.equal(want) {
		t.Fatalf("filtered selection = %#v, want agent row %#v", got, want)
	}
}

func TestFilterSessionsIncrementalMatchesColdSearchOrder(t *testing.T) {
	sessions := []session.Session{
		{Name: "workspace_0042_api"},
		{Name: "workspace_0420_api"},
		{Name: "api-42"},
		{Name: "archive"},
		{Name: "workbench"},
	}

	cold := newTestModel()

	cold.sessions = append([]session.Session(nil), sessions...)
	cold.textInput.SetValue("42")
	cold.filterSessions()

	incremental := newTestModel()

	incremental.sessions = append([]session.Session(nil), sessions...)
	incremental.textInput.SetValue("4")
	incremental.filterSessions()
	incremental.textInput.SetValue("42")
	incremental.filterSessions()

	got := sessionNamesForTest(incremental.filtered)

	want := sessionNamesForTest(cold.filtered)
	if !slicesEqual(got, want) {
		t.Fatalf("incremental filtered sessions = %#v, want cold order %#v", got, want)
	}
}

func TestFilterSessionsMultiFieldFuzzySearch(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{
			Name:             "backend-service",
			ActivePanePath:   "/work/backend/server",
			ActiveWindowName: "editor",
			ActivePaneTitle:  "server.go",
			Agents: []session.AgentSession{
				{
					Harness:        "omp",
					HarnessSession: "omp-server-agent",
					Status:         session.AgentStatusRunning,
					ProjectRoot:    "/work/backend",
				},
			},
		},
		{
			Name:             "web-app",
			ActivePanePath:   "/work/web/ui",
			ActiveWindowName: "vite",
			Agents: []session.AgentSession{
				{
					Harness:        "pi",
					HarnessSession: "pi-ui-agent",
					Status:         session.AgentStatusIdle,
					ProjectRoot:    "/work/web",
				},
			},
		},
		{
			Name:             "infra-tools",
			ActivePanePath:   "/work/infra/terraform",
			ActiveWindowName: "terminal",
			Agents: []session.AgentSession{
				{
					Harness:        "codex",
					HarnessSession: "terraform-plan",
					Status:         session.AgentStatusFailed,
				},
				{
					Harness:        "claude",
					HarnessSession: "infra-review",
					Status:         session.AgentStatusInterrupted,
				},
			},
		},
	}
	model.refreshDisplaySessions()

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "match by harness name omp",
			query: "omp",
			want:  []string{"backend-service"},
		},
		{
			name:  "match by harness name pi",
			query: "pi",
			want:  []string{"web-app"},
		},
		{
			name:  "match by agent status working",
			query: "working",
			want:  []string{"backend-service"},
		},
		{
			name:  "match by agent status live",
			query: "live",
			want:  []string{"backend-service"},
		},
		{
			name:  "match by agent status active",
			query: "active",
			want:  []string{"backend-service"},
		},
		{
			name:  "match by agent status idle",
			query: "idle",
			want:  []string{"web-app"},
		},
		{
			name:  "match by path terraform",
			query: "terraform",
			want:  []string{"infra-tools"},
		},
		{
			name:  "match by window vite",
			query: "vite",
			want:  []string{"web-app"},
		},
		{
			name:  "match by agent session name",
			query: "omp-server",
			want:  []string{"backend-service"},
		},
		{
			name:  "match by agent status failed",
			query: "failed",
			want:  []string{"infra-tools"},
		},
		{
			name:  "match by agent status paused",
			query: "paused",
			want:  []string{"infra-tools"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model.textInput.SetValue(tt.query)
			model.filterSessions()

			got := sessionNamesForTest(model.filtered)
			if len(got) == 0 || got[0] != tt.want[0] {
				t.Fatalf("query %q filtered = %#v, want top match %q", tt.query, got, tt.want[0])
			}
		})
	}
}

func TestAgentsModeFiltersChildRowsToMatchingAgents(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{
			Name: "main-session",
			Agents: []session.AgentSession{
				{Harness: "pi", HarnessSession: "pi-ext-perf", Status: session.AgentStatusIdle},
				{Harness: "pi", HarnessSession: "Fix Oxlint Errors", Status: session.AgentStatusRunning},
				{Harness: "omp", HarnessSession: "Integrate Rust rewrite", Status: session.AgentStatusIdle},
				{Harness: "omp", HarnessSession: "Add activity states", Status: session.AgentStatusWaiting},
			},
		},
		{
			Name: "other-session",
			Agents: []session.AgentSession{
				{Harness: "claude", HarnessSession: "claude-review", Status: session.AgentStatusIdle},
			},
		},
		{
			Name: "plain-session",
		},
	}
	model.refreshDisplaySessions()

	t.Run("agent task", func(t *testing.T) {
		assertAgentTaskFilter(t, model)
	})
	t.Run("agent harness", func(t *testing.T) {
		assertAgentHarnessFilter(t, model)
	})
	t.Run("session name", func(t *testing.T) {
		assertAgentSessionNameFilter(t, model)
	})
	t.Run("regular session", func(t *testing.T) {
		assertRegularSessionFilter(t, model)
	})
}

func assertAgentTaskFilter(t *testing.T, model *Model) {
	t.Helper()

	model.textInput.SetValue("oxlint")
	model.filterSessions()

	if len(model.filtered) != 1 {
		t.Fatalf("filtered sessions count = %d, want 1", len(model.filtered))
	}

	if len(model.filtered[0].Agents) != 1 || model.filtered[0].Agents[0].HarnessSession != "Fix Oxlint Errors" {
		t.Fatalf("filtered agents = %#v, want only Fix Oxlint Errors", model.filtered[0].Agents)
	}

	if rows := model.popupListRows(); len(rows) != 2 {
		t.Fatalf("visual list rows count = %d, want 2 (parent + 1 child): %#v", len(rows), rows)
	}
}

func assertAgentHarnessFilter(t *testing.T, model *Model) {
	t.Helper()

	model.textInput.SetValue("omp")
	model.filterSessions()

	if len(model.filtered) != 1 {
		t.Fatalf("filtered sessions count = %d, want 1", len(model.filtered))
	}

	if len(model.filtered[0].Agents) != 2 {
		t.Fatalf("filtered agents count = %d, want 2 omp agents: %#v", len(model.filtered[0].Agents), model.filtered[0].Agents)
	}

	for _, agent := range model.filtered[0].Agents {
		if agent.Harness != "omp" {
			t.Fatalf("unexpected non-omp agent: %#v", agent)
		}
	}
}

func assertAgentSessionNameFilter(t *testing.T, model *Model) {
	t.Helper()

	model.textInput.SetValue("main-session")
	model.filterSessions()

	if len(model.filtered) != 1 {
		t.Fatalf("filtered sessions count = %d, want 1", len(model.filtered))
	}

	if len(model.filtered[0].Agents) != 4 {
		t.Fatalf("filtered agents when searching session name = %#v, want all 4 agents", model.filtered[0].Agents)
	}
}

func assertRegularSessionFilter(t *testing.T, model *Model) {
	t.Helper()

	model.textInput.SetValue("plain")
	model.filterSessions()

	if len(model.filtered) != 1 || model.filtered[0].Name != "plain-session" {
		t.Fatalf("filtered sessions when searching regular = %#v, want plain-session", model.filtered)
	}
}

func TestFilterSessionsMatchesCWDAndProjectRoot(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{
			Name: "my-app",
			Agents: []session.AgentSession{
				{
					Harness:     "codex",
					ProjectRoot: "/Users/dev/cool-project",
					CWD:         "/Users/dev/cool-project/subpkg",
				},
			},
		},
		{
			Name: "other-app",
		},
	}
	model.refreshDisplaySessions()

	model.textInput.SetValue("cool-project")
	model.filterSessions()

	got := sessionNamesForTest(model.filtered)
	if len(got) != 1 || got[0] != "my-app" {
		t.Fatalf("search for cool-project = %#v, want [my-app]", got)
	}

	model.textInput.SetValue("subpkg")
	model.filterSessions()

	got = sessionNamesForTest(model.filtered)
	if len(got) != 1 || got[0] != "my-app" {
		t.Fatalf("search for subpkg = %#v, want [my-app]", got)
	}
}

func TestFilterSessionsMatchesActivePaneTitle(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{
		{
			Name:            "workspace-1",
			ActivePaneTitle: "debugging database deadlock",
		},
		{
			Name:            "workspace-2",
			ActivePaneTitle: "editing README",
		},
	}
	model.refreshDisplaySessions()

	model.textInput.SetValue("deadlock")
	model.filterSessions()

	got := sessionNamesForTest(model.filtered)
	if len(got) != 1 || got[0] != "workspace-1" {
		t.Fatalf("search for deadlock = %#v, want [workspace-1]", got)
	}
}

func TestFilterSessionsSeparatorVisibilityWhenFiltering(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.sessions = []session.Session{
		{
			Name: "agent-session",
			Agents: []session.AgentSession{
				{Harness: "codex", HarnessSession: "agent-codex"},
			},
		},
		{
			Name: "regular-session",
		},
	}
	model.refreshDisplaySessions()

	model.filterSessions()

	hasSeparator := false

	for _, row := range model.popupListRows() {
		if row.isSeparator() {
			hasSeparator = true
			break
		}
	}

	if !hasSeparator {
		t.Fatal("unfiltered agents view missing separator between agent and regular session")
	}

	model.textInput.SetValue("regular")
	model.filterSessions()

	for _, row := range model.popupListRows() {
		if row.isSeparator() {
			t.Fatal("filtered agents view unexpectedly shows separator when only regular sessions match")
		}
	}
}

func testSessions(count int) []session.Session {
	sessions := make([]session.Session, count)
	for i := range count {
		sessions[i] = session.Session{
			Name: fmt.Sprintf("session-%d", i),
			Kind: session.KindLive,
		}
	}

	return sessions
}

func BenchmarkFilterSessionsLargeList(b *testing.B) {
	model := newTestModel()

	model.sessions = make([]session.Session, 5000)
	for i := range model.sessions {
		model.sessions[i] = session.Session{Name: fmt.Sprintf("workspace_%04d_api", i)}
	}

	model.refreshDisplaySessions()

	model.textInput.SetValue("42")

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model.filterSessions()

		if len(model.filtered) == 0 {
			b.Fatal("filterSessions() returned no matches")
		}
	}
}

func BenchmarkFilterSessionsLargeListIncremental(b *testing.B) {
	model := newTestModel()

	model.sessions = make([]session.Session, 5000)
	for i := range model.sessions {
		model.sessions[i] = session.Session{Name: fmt.Sprintf("workspace_%04d_api", i)}
	}

	model.refreshDisplaySessions()

	model.textInput.SetValue("4")
	model.filterSessions()
	baseFiltered := append([]session.Session(nil), model.filtered...)
	model.textInput.SetValue("42")

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		model.filtered = baseFiltered
		model.filterSessions()

		if len(model.filtered) == 0 {
			b.Fatal("filterSessions() returned no matches")
		}
	}
}
