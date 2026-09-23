//go:build linux || darwin

package agents_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/agents"
)

func BenchmarkLoadLiveAgentsLargeRegistry(b *testing.B) {
	const (
		sessionCount = 1500
		liveCount    = 6
	)

	root := b.TempDir()
	b.Setenv("HOME", root)
	b.Setenv("XDG_STATE_HOME", root)
	b.Setenv("XDG_RUNTIME_DIR", root)

	sessions := make(map[string]any, sessionCount)

	for index := range sessionCount {
		fixture := registryFixture{
			ID:        fmt.Sprintf("agent-%04d", index),
			Harness:   "codex",
			Presence:  "gone",
			UpdatedAt: "2026-09-22T00:00:00Z",
		}
		if index < liveCount {
			fixture.Presence = "live"
			fixture.Activity = "idle"
			fixture.SessionID = fmt.Sprintf("$%d", index)
			fixture.PaneID = fmt.Sprintf("%%%d", index)
		}

		record := registryRecord(fixture)
		record["observations"] = map[string]any{
			"native": map[string]any{
				"observed_at": fixture.UpdatedAt,
				"raw_payload": map[string]string{"padding": strings.Repeat("x", 700)},
			},
		}
		sessions[fixture.ID] = record
	}

	data, err := json.Marshal(map[string]any{"schema_version": 2, "revision": 1, "sessions": sessions})
	if err != nil {
		b.Fatal(err)
	}

	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		b.Fatal(err)
	}

	source := agents.NewAHTStore(path)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		got, err := agents.LoadLiveAgents(b.Context(), source)
		if err != nil {
			b.Fatal(err)
		}

		if len(got) != liveCount {
			b.Fatalf("live agent count = %d, want %d", len(got), liveCount)
		}
	}
}
