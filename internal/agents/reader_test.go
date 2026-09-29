//go:build linux || darwin

package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

type registryFixture struct {
	ID               string
	Harness          string
	Presence         string
	Activity         string
	ServerSocket     string
	SessionID        string
	SessionName      string
	WindowID         string
	PaneID           string
	PanePID          int
	HarnessSessionID string
	SessionPath      string
	ResumeCommand    []string
	ProcessPID       int
	StartIdentity    string
	NativeMode       string
	NativeProcessPID int
	UpdatedAt        string
}

func writeRegistry(t *testing.T, fixtures ...registryFixture) string {
	t.Helper()

	sessions := make(map[string]any, len(fixtures))

	for index, fixture := range fixtures {
		id := registryFixtureID(fixture, index)
		if fixture.ID == "" {
			fixture.ID = id
		}

		sessions[id] = registryRecord(fixture)
	}

	document := map[string]any{
		"schema_version": 3,
		"revision":       1,
		"sessions":       sessions,
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encoding registry fixture: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("writing registry fixture: %v", err)
	}

	return path
}

func registryFixtureID(fixture registryFixture, index int) string {
	if fixture.ID != "" {
		return fixture.ID
	}

	return "agent-" + strconv.Itoa(index)
}

func registryRecord(fixture registryFixture) map[string]any {
	updatedAt := fixture.UpdatedAt
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	presence := defaultString(fixture.Presence, string(aht.PresenceLive))
	liveness := map[string]any{
		"kind": presence,
	}

	if presence != string(aht.PresenceGone) {
		act := fixture.Activity
		if act == "" {
			act = string(aht.ActivityIdle)
		}

		liveness["activity"] = act
	}

	record := map[string]any{
		"schema_version":      3,
		"id":                  fixture.ID,
		"harness":             defaultString(fixture.Harness, string(aht.HarnessOmp)),
		"liveness":            liveness,
		"observations":        map[string]any{},
		"created_at":          updatedAt,
		"updated_at":          updatedAt,
		"presence_changed_at": updatedAt,
		"activity_changed_at": updatedAt,
	}

	addOptionalStrings(record, map[string]string{
		"session_id":   fixture.HarnessSessionID,
		"session_path": fixture.SessionPath,
	})

	if len(fixture.ResumeCommand) > 0 {
		record["resume_command"] = fixture.ResumeCommand
	}

	if location := locationRecord(fixture); location != nil {
		record["location"] = location
	}

	if process := processRecord(fixture); process != nil {
		record["process"] = process
	}

	if native := nativeRecord(fixture, updatedAt); native != nil {
		record["observations"] = map[string]any{"native": native}
	}

	return record
}

func locationRecord(fixture registryFixture) map[string]any {
	if fixture.PaneID == "" && fixture.ServerSocket == "" && fixture.SessionID == "" {
		return nil
	}

	location := map[string]any{
		"kind": string(aht.MultiplexerTmux),
	}

	addOptionalStrings(location, map[string]string{
		"server_id":    fixture.ServerSocket,
		"session_id":   fixture.SessionID,
		"session_name": fixture.SessionName,
		"window_id":    fixture.WindowID,
		"pane_id":      fixture.PaneID,
	})

	if fixture.PanePID != 0 {
		location["pane_pid"] = fixture.PanePID
	}

	return location
}

func processRecord(fixture registryFixture) map[string]any {
	if fixture.ProcessPID == 0 || fixture.StartIdentity == "" {
		return nil
	}

	return map[string]any{
		"pid":            fixture.ProcessPID,
		"start_identity": fixture.StartIdentity,
	}
}

func nativeRecord(fixture registryFixture, observedAt string) map[string]any {
	if fixture.NativeMode == "" && fixture.NativeProcessPID == 0 {
		return nil
	}

	attributes := map[string]any{}

	if fixture.NativeMode != "" {
		attributes["aht_interaction_mode"] = fixture.NativeMode
	}

	nativeProcessPID := fixture.NativeProcessPID
	if nativeProcessPID == 0 {
		nativeProcessPID = fixture.ProcessPID
	}

	return map[string]any{
		"attributes":  attributes,
		"observed_at": observedAt,
		"process": map[string]any{
			"pid":            nativeProcessPID,
			"start_identity": fixture.StartIdentity,
		},
	}
}

func addOptionalStrings(target map[string]any, values map[string]string) {
	for key, value := range values {
		if value != "" {
			target[key] = value
		}
	}
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}

func loadFixtureAgents(t *testing.T, path string) []agents.AgentSession {
	t.Helper()

	loaded, err := agents.LoadLiveAgents(t.Context(), agents.NewAHTStore(path))
	if err != nil {
		t.Fatalf("LoadLiveAgents failed: %v", err)
	}

	return loaded
}

func TestLoadLiveAgentsReadsRegistryThroughAHT(t *testing.T) {
	t.Parallel()

	path := writeRegistry(t,
		registryFixture{
			ID:               "waiting-agent",
			Harness:          string(aht.HarnessOmp),
			Activity:         string(aht.ActivityWaiting),
			ServerSocket:     "/tmp/fixture/socket",
			SessionID:        "$1",
			SessionName:      "dev",
			WindowID:         "@1",
			PaneID:           "%1",
			PanePID:          4242,
			HarnessSessionID: "conv-1",
			SessionPath:      "/home/user/.omp/sessions/conv-1.jsonl",
			ResumeCommand:    []string{"omp", "--resume", "conv-1"},
			UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		},
		registryFixture{
			ID:           "gone-agent",
			Presence:     string(aht.PresenceGone),
			SessionID:    "$1",
			SessionName:  "dev",
			WindowID:     "@1",
			PaneID:       "%2",
			ServerSocket: "/tmp/fixture/socket",
		},
		registryFixture{
			ID:           "paneless-agent",
			Activity:     string(aht.ActivityIdle),
			SessionID:    "$1",
			SessionName:  "dev",
			ServerSocket: "/tmp/fixture/socket",
		},
	)

	loaded := loadFixtureAgents(t, path)

	if len(loaded) != 1 {
		t.Fatalf("live agent count = %d, want 1", len(loaded))
	}

	agent := loaded[0]

	if agent.ID != "waiting-agent" || agent.Attention != "waiting" {
		t.Fatalf("unexpected agent identity/attention: %+v", agent)
	}

	if agent.ServerSocket != "/tmp/fixture/socket" || agent.PaneID != "%1" || agent.PanePID != 4242 {
		t.Fatalf("unexpected tmux coordinates: %+v", agent)
	}

	if agent.HarnessSessionID != "conv-1" || len(agent.ResumeCommand) != 3 {
		t.Fatalf("native resume metadata missing: %+v", agent)
	}

	if !agent.Interactive {
		t.Fatalf("expected interactive agent: %+v", agent)
	}
}

func TestObservedAgentSessionsUsesPublishedNativeObservation(t *testing.T) {
	at := time.Unix(500, 0)
	sessions := []aht.Session{{
		ID: "observed", Harness: aht.HarnessClaude, Liveness: aht.Live{Activity: aht.ActivityWaiting},
		Location: aht.Location{Kind: aht.MultiplexerTmux, PaneID: "%1"},
		Observations: registry.Observations{Native: &registry.NativeObservation{
			Event: "UserPromptSubmit", ObservedAt: at,
			Attributes: map[string]string{
				"claude_model": "opus", "claude_notification_type": "permission_prompt", "claude_tool_name": "Bash",
			},
		}},
	}}

	observed := agents.ObservedAgentSessions(sessions)
	if len(observed) != 1 || observed[0].Model != "opus" || observed[0].WaitingReason != "permission" ||
		observed[0].LastTool != "Bash" || !observed[0].PromptAt.Equal(at) {
		t.Fatalf("native observation = %+v", observed)
	}

	sessions[0].Observations.Native = &registry.NativeObservation{Event: "Stop", ObservedAt: at.Add(time.Second)}
	if next := agents.ObservedAgentSessions(sessions); len(next) != 1 || next[0].Model != "" || !next[0].PromptAt.IsZero() {
		t.Fatalf("later observation retained unsupported metadata = %+v", next)
	}
}

func TestLoadLiveAgentsMissingRegistryIsEmpty(t *testing.T) {
	t.Parallel()

	loaded := loadFixtureAgents(t, filepath.Join(t.TempDir(), "absent", "state.json"))

	if len(loaded) != 0 {
		t.Fatalf("agent count = %d, want 0 for an absent registry", len(loaded))
	}
}

func TestLoadLiveAgentsUnsupportedSchemaFailsClosed(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 99, "sessions": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := agents.LoadLiveAgents(t.Context(), agents.NewAHTStore(path))
	if !errors.Is(err, agents.ErrRegistrySchema) {
		t.Fatalf("error = %v, want ErrRegistrySchema", err)
	}
}

func TestLoadLiveAgentsCorruptRegistryFailsClosed(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 3, "sessions": [`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := agents.LoadLiveAgents(t.Context(), agents.NewAHTStore(path))
	if !errors.Is(err, agents.ErrRegistryCorrupt) {
		t.Fatalf("error = %v, want ErrRegistryCorrupt", err)
	}
}

func TestLoadLiveAgentsRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := agents.LoadLiveAgents(ctx, agents.NewAHTStore(writeRegistry(t)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestHeadlessSessionsAreNotAgents(t *testing.T) {
	t.Parallel()

	path := writeRegistry(t,
		registryFixture{
			ID:            "headless-agent",
			Activity:      string(aht.ActivityIdle),
			ServerSocket:  "/tmp/fixture/socket",
			SessionID:     "$1",
			SessionName:   "dev",
			WindowID:      "@1",
			PaneID:        "%1",
			PanePID:       4242,
			ProcessPID:    4242,
			StartIdentity: "boot:42",
			NativeMode:    "print",
		},
		registryFixture{
			ID:            "interactive-agent",
			Activity:      string(aht.ActivityIdle),
			ServerSocket:  "/tmp/fixture/socket",
			SessionID:     "$1",
			SessionName:   "dev",
			WindowID:      "@1",
			PaneID:        "%2",
			PanePID:       4343,
			ProcessPID:    4343,
			StartIdentity: "boot:43",
			NativeMode:    "tui",
		},
	)

	loaded := loadFixtureAgents(t, path)

	if len(loaded) != 1 || loaded[0].ID != "interactive-agent" {
		t.Fatalf("headless filtering produced %+v", loaded)
	}
}

func TestPiRequiresCurrentProcessTUIMode(t *testing.T) {
	t.Parallel()

	current := registryFixture{
		ID:            "pi-current",
		Harness:       string(aht.HarnessPi),
		Activity:      string(aht.ActivityIdle),
		ServerSocket:  "/tmp/fixture/socket",
		SessionID:     "$1",
		SessionName:   "dev",
		WindowID:      "@1",
		PaneID:        "%1",
		PanePID:       4242,
		ProcessPID:    4242,
		StartIdentity: "boot:42",
		NativeMode:    "tui",
	}

	restarted := current
	restarted.ID = "pi-restarted"
	restarted.PaneID = "%2"
	restarted.NativeProcessPID = 4242
	restarted.ProcessPID = 4343
	restarted.StartIdentity = "boot:43"

	loaded := loadFixtureAgents(t, writeRegistry(t, current, restarted))

	if len(loaded) != 1 || loaded[0].ID != "pi-current" {
		t.Fatalf("pi interaction filtering produced %+v", loaded)
	}
}

func TestSummarizeAgentsCountsAttention(t *testing.T) {
	t.Parallel()

	path := writeRegistry(t,
		registryFixture{ID: "a1", Activity: string(aht.ActivityWaiting), ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%1"},
		registryFixture{ID: "a2", Activity: string(aht.ActivityFailed), ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%2"},
		registryFixture{ID: "a3", Activity: string(aht.ActivityIdle), ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%3"},
		registryFixture{ID: "a4", Activity: string(aht.ActivityRunning), ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%4"},
		registryFixture{ID: "a5", Activity: string(aht.ActivityInterrupted), ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%5"},
	)

	summary, err := agents.Status(t.Context(), path)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if summary.Total != 5 || summary.Live != 5 {
		t.Fatalf("summary total/live = %d/%d, want 5/5", summary.Total, summary.Live)
	}

	if summary.Waiting != 1 || summary.Failed != 1 || summary.Idle != 1 || summary.Running != 1 || summary.Interrupted != 1 {
		t.Fatalf("summary attention counts = %+v", summary)
	}

	if summary.Unread != 0 {
		t.Fatalf("registry status cannot report unread, got %d", summary.Unread)
	}
}

func TestPruneVanishedPanesDropsReusedAndDeadPanes(t *testing.T) {
	t.Parallel()

	observed := []agents.AgentSession{
		{ID: "live", ServerSocket: "/s", PaneID: "%1", PanePID: 100},
		{ID: "dead", ServerSocket: "/s", PaneID: "%2", PanePID: 200},
		{ID: "reused", ServerSocket: "/s", PaneID: "%3", PanePID: 300},
		{ID: "other-server", ServerSocket: "/other", PaneID: "%4", PanePID: 400},
		{ID: "paneless"},
	}

	panes := []agents.PaneLocation{
		{ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%1", PanePID: 100},
		{ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", PaneID: "%3", PanePID: 999},
		{ServerSocket: "/other", SessionID: "$9", SessionName: "other", WindowID: "@9", PaneID: "%4", PanePID: 400},
	}

	kept := agents.PruneVanishedPanes(observed, panes)

	ids := make([]string, 0, len(kept))
	for _, agent := range kept {
		ids = append(ids, agent.ID)
	}

	want := []string{"live", "other-server", "paneless"}
	if len(ids) != len(want) {
		t.Fatalf("kept agents = %v, want %v", ids, want)
	}

	for index, id := range want {
		if ids[index] != id {
			t.Fatalf("kept agents = %v, want %v", ids, want)
		}
	}
}

func TestAttachPanePositionsUsesLiveGeometry(t *testing.T) {
	t.Parallel()

	observed := []agents.AgentSession{
		{ID: "live", ServerSocket: "/s", PaneID: "%1", PanePID: 100},
		{ID: "unknown", ServerSocket: "/s", PaneID: "%9", PanePID: 900},
	}

	panes := []agents.PaneLocation{
		{ServerSocket: "/s", SessionID: "$1", SessionName: "dev", WindowID: "@1", WindowIndex: "2", PaneID: "%1", PaneIndex: "1", PanePID: 100},
	}

	positioned := agents.AttachPanePositions(observed, panes)

	if positioned[0].WindowIndex != "2" || positioned[0].PaneIndex != "1" {
		t.Fatalf("live agent position = %+v", positioned[0])
	}

	if positioned[1].WindowIndex != "" || positioned[1].PaneIndex != "" {
		t.Fatalf("unobserved agent must keep unknown geometry: %+v", positioned[1])
	}
}

func TestSameServerSocketComparesFullPaths(t *testing.T) {
	t.Parallel()

	if agents.SameServerSocket("/tmp/server-a/tmux-1000/default", "/tmp/server-b/tmux-1000/default") {
		t.Fatal("distinct full socket paths must not collide")
	}

	if !agents.SameServerSocket("default", "/tmp/server-a/tmux-1000/default") {
		t.Fatal("bare server names must match their full path")
	}
}
