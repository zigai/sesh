//go:build linux || darwin

package cmdpicker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/session"
)

func TestPickerServicesLoadAndWatchPiConversationNames(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "conversation.jsonl")
	writePickerNameFixture(t, transcript, `{"type":"session_info","name":"Loaded name"}`+"\n")

	statePath := filepath.Join(dir, "state.json")
	now := time.Now().UTC()
	identity := registry.ProcessIdentity{PID: 42, StartIdentity: "fixture:42"}
	agent := aht.Session{
		SchemaVersion: 3,
		ID:            "registry-id", Harness: aht.HarnessPi,
		Liveness:  aht.Live{Activity: aht.ActivityIdle},
		SessionID: "native-resume-id", SessionPath: transcript, Process: &identity,
		ResumeCommand: []string{"pi", "--resume", "native-resume-id"},
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    "/tmp/tmux.sock",
			SessionID:   "$1",
			SessionName: "workspace",
			PaneID:      "%1",
			PanePID:     42,
		},
		Observations: registry.Observations{Native: &registry.NativeObservation{
			Process: identity, Attributes: map[string]string{"aht_interaction_mode": "tui"}, ObservedAt: now,
		}},
		CreatedAt: now, UpdatedAt: now, PresenceChangedAt: now, ActivityChangedAt: now,
	}
	writePickerAgentRegistry(t, statePath, agent)

	services := &pickerServices{agentStore: agents.NewAHTStore(statePath)}

	summaries, err := services.LoadAgentSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	assertPickerServiceName(t, summaries, "Loaded name")
	appendPickerNameFixture(t, transcript, `{"type":"session_info","name":"Watched name"}`+"\n")

	store := services.agentStore
	services.agentStore = pickerNameWatchSource{sessions: []aht.Session{agent}}

	published := 0

	err = services.WatchAgentSummaries(t.Context(), func(_ uint64, summaries []session.AgentSummary, publishErr error) error {
		if publishErr != nil {
			return publishErr
		}

		want := "Watched name"
		if published > 0 {
			want = "Later name"
		}

		assertPickerServiceName(t, summaries, want)

		published++

		appendPickerNameFixture(t, transcript, `{"type":"session_info","name":"Later name"}`+"\n")

		return nil
	})
	if err != nil || published != 2 {
		t.Fatalf("watch result: published=%d err=%v", published, err)
	}

	stored, err := store.List(t.Context(), aht.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(stored) != 1 || stored[0].SessionID != "native-resume-id" || stored[0].ResumeCommand[2] != "native-resume-id" {
		t.Fatalf("native resume metadata changed: %#v", stored)
	}

	if services.sessionNames.cache[transcript].offset == 0 {
		t.Fatal("picker did not retain transcript progress between load and watch")
	}
}

func TestPickerServicesLoadCodexAndOmpNativeTitles(t *testing.T) {
	root := t.TempDir()

	codexHome := filepath.Join(root, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(`{"id":"codex-native","thread_name":"Codex project plan"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ompPath := filepath.Join(root, "omp.jsonl")
	if err := os.WriteFile(ompPath, []byte(`{"type":"title","title":"OMP design review"}`+"\n"+`{"type":"session","id":"omp-native"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	base := aht.Session{
		SchemaVersion: 3,
		Liveness:      aht.Live{Activity: aht.ActivityIdle},
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    "/tmp/tmux.sock",
			SessionID:   "$1",
			SessionName: "workspace",
			PaneID:      "%1",
		},
	}
	codex := base
	codex.ID, codex.Harness, codex.SessionID = "codex-registry", aht.HarnessCodex, "codex-native"
	codex.SessionPath = filepath.Join(codexHome, "sessions", "2026", "rollout.jsonl")
	omp := base
	omp.ID, omp.Harness, omp.SessionID, omp.SessionPath = "omp-registry", aht.HarnessOmp, "omp-native", ompPath
	omp.Location.PaneID = "%2"

	services := &pickerServices{agentStore: pickerNameWatchSource{sessions: []aht.Session{codex, omp}}}

	summaries, err := services.LoadAgentSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	titles := make(map[string][2]string)

	for _, summary := range summaries {
		for _, agent := range summary.Agents {
			titles[agent.RegistryID] = [2]string{agent.HarnessSession, agent.Task}
		}
	}

	want := map[string][2]string{
		"codex-registry": {"Codex project plan", "Codex project plan"},
		"omp-registry":   {"OMP design review", "OMP design review"},
	}
	if diff := cmp.Diff(want, titles); diff != "" {
		t.Fatalf("native titles and tasks mismatch (-want +got):\n%s", diff)
	}

	if codex.SessionID != "codex-native" || omp.SessionID != "omp-native" {
		t.Fatal("native resume identities changed")
	}
}

func writePickerAgentRegistry(t *testing.T, path string, agent aht.Session) {
	t.Helper()

	document := map[string]any{"schema_version": 3, "sessions": map[string]aht.Session{agent.ID: agent}}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

type pickerNameWatchSource struct {
	sessions []aht.Session
}

func (s pickerNameWatchSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return s.sessions, nil
}

func (s pickerNameWatchSource) Watch(
	ctx context.Context,
	_ aht.Filter,
	publish func(aht.StateSnapshot) error,
) error {
	for revision := range uint64(2) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("watch fixture canceled: %w", err)
		}

		if err := publish(aht.StateSnapshot{Revision: revision + 1, Sessions: s.sessions}); err != nil {
			return err
		}
	}

	return nil
}

func pickerServicesStorePath(t *testing.T, services *pickerServices) string {
	t.Helper()

	store, ok := services.agentStore.(*agents.AHTStore)
	if !ok {
		t.Fatalf("picker source is %T, want AHT store", services.agentStore)
	}

	return store.StorePath()
}

func assertPickerServiceName(t *testing.T, summaries []session.AgentSummary, want string) {
	t.Helper()

	if len(summaries) != 1 || len(summaries[0].Agents) != 1 || summaries[0].Agents[0].HarnessSession != want {
		t.Fatalf("picker summaries = %#v, want one agent named %q", summaries, want)
	}
}

func TestPickerServicesAHTStorePathRouting(t *testing.T) {
	ctx := t.Context()
	dirs := t.TempDir()

	t.Run("explicit agent state path takes precedence", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.AgentStatus.StorePath = "/config/aht.json"

		services, err := newPickerServices(
			ctx,
			cfg,
			filepath.Join(dirs, "tmux.sock"),
			filepath.Join(dirs, "repo1"),
			filepath.Join(dirs, "leases1"),
			filepath.Join(dirs, "history1.json"),
			"/explicit/aht.json",
			gotmux.TerminalStreams{},
			PickerRemoteServices{},
		)
		if err != nil {
			t.Fatalf("newPickerServices failed: %v", err)
		}

		t.Cleanup(func() { _ = services.Close() })

		if path := pickerServicesStorePath(t, services); path != "/explicit/aht.json" {
			t.Fatalf("StorePath = %q, want /explicit/aht.json", path)
		}
	})

	t.Run("falls back to configured agent status store path", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.AgentStatus.StorePath = "/config/fallback/aht.json"

		services, err := newPickerServices(
			ctx,
			cfg,
			filepath.Join(dirs, "tmux.sock"),
			filepath.Join(dirs, "repo2"),
			filepath.Join(dirs, "leases2"),
			filepath.Join(dirs, "history2.json"),
			"",
			gotmux.TerminalStreams{},
			PickerRemoteServices{},
		)
		if err != nil {
			t.Fatalf("newPickerServices failed: %v", err)
		}

		t.Cleanup(func() { _ = services.Close() })

		if path := pickerServicesStorePath(t, services); path != "/config/fallback/aht.json" {
			t.Fatalf("StorePath = %q, want /config/fallback/aht.json", path)
		}
	})
}

func TestPickerServicesSnapshotSessionRejectsInvalidEnv(t *testing.T) {
	ctx := t.Context()
	dirs := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Snapshot.ProcessEnvironment = []string{"INVALID=KEY"}

	services, err := newPickerServices(
		ctx,
		cfg,
		filepath.Join(dirs, "tmux.sock"),
		filepath.Join(dirs, "repo"),
		filepath.Join(dirs, "leases"),
		filepath.Join(dirs, "history.json"),
		"",
		gotmux.TerminalStreams{},
		PickerRemoteServices{},
	)
	if err != nil {
		t.Fatalf("newPickerServices failed: %v", err)
	}

	t.Cleanup(func() { _ = services.Close() })

	target := session.Session{ID: "0", Name: "test"}

	err = services.SnapshotSession(ctx, target)
	if err == nil {
		t.Fatal("expected SnapshotSession to reject invalid environment key")
	}

	if !errors.Is(err, process.ErrInvalidVariableName) {
		t.Fatalf("expected ErrInvalidVariableName, got: %v", err)
	}
}

func TestPickerServicesSnapshotSessionRejectsExcessiveEnv(t *testing.T) {
	ctx := t.Context()
	dirs := t.TempDir()

	keys := make([]string, 257)
	for i := range keys {
		keys[i] = fmt.Sprintf("K%d", i)
	}

	cfg := config.DefaultConfig()
	cfg.Snapshot.ProcessEnvironment = keys

	services, err := newPickerServices(
		ctx,
		cfg,
		filepath.Join(dirs, "tmux.sock"),
		filepath.Join(dirs, "repo"),
		filepath.Join(dirs, "leases"),
		filepath.Join(dirs, "history.json"),
		"",
		gotmux.TerminalStreams{},
		PickerRemoteServices{},
	)
	if err != nil {
		t.Fatalf("newPickerServices failed: %v", err)
	}

	t.Cleanup(func() { _ = services.Close() })

	target := session.Session{ID: "0", Name: "test"}

	err = services.SnapshotSession(ctx, target)
	if err == nil {
		t.Fatal("expected SnapshotSession to reject excessive environment keys")
	}

	if !errors.Is(err, process.ErrEnvironmentLimitExceeded) {
		t.Fatalf("expected ErrEnvironmentLimitExceeded, got: %v", err)
	}
}
