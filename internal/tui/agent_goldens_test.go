package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestDefaultAgentRowsMatchGoldens(t *testing.T) {
	for _, width := range []int{48, 72, 96, 128} {
		for _, style := range []string{"glyph", "text"} {
			t.Run(fmt.Sprintf("%d/%s", width, style), func(t *testing.T) {
				model := newTestModelWithConfig(t, config.DefaultConfig())
				model.displayMode = displayModeAgents
				model.agentCursor = parentAgentIndex
				model.config.AgentSessions.StatusStyle = style
				model.filtered = []session.Session{{
					ID: "$1", Name: "project", AgentTotal: 2, AgentActive: 1, ActivePaneID: "%1",
					Agents: []session.AgentSession{
						{PaneID: "%1", Harness: "codex", HarnessSession: "core-worker-long-name", CWD: "/work/alpha", Status: session.AgentStatusRunning, Worktree: "/work/repo", GitBranch: "feature/grid", GitChanges: "+150 -2", GitAhead: 150, GitBehind: 3, GitTrackingKnown: true},
						{PaneID: "%2", Harness: "claude", HarnessSession: "short", CWD: "/tmp", Status: session.AgentStatusIdle},
					},
				}}

				got := stripANSI(model.renderList(width, 3))

				path := filepath.Join("testdata", "agent_rows", fmt.Sprintf("default_%d_%s.golden", width, style))
				if os.Getenv("SESH_UPDATE_AGENT_GOLDENS") == "1" {
					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}

				if got != string(want) {
					t.Fatalf("default row layout changed\ngot:  %q\nwant: %q", got, string(want))
				}
			})
		}
	}
}
