//go:build linux && integration

package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentsPopupInitialIdleIsReadAndCompletedTurnIsUnread(t *testing.T) {
	requirePickerTermtrace(t)
	clearAmbientTmux(t)

	root := pickerIntegrationRoot(t)
	fixture := newCLISessionFixture(t)
	idle := registry.ActivityIdle
	records := make(map[string]any, 2)

	for _, event := range []string{"SessionStart", "Stop"} {
		live := fixture.newLiveSession(t, "idle-"+event)

		panes, err := live.Handle().Panes(t.Context())
		if err != nil || len(panes) != 1 {
			t.Fatalf("idle session panes: %v", err)
		}

		pane := panes[0]
		now := time.Now().UTC()
		records[event] = map[string]any{
			"schema_version": 3, "id": event, "harness": "codex",
			"liveness":   map[string]any{"kind": "live", "activity": "idle"},
			"session_id": event, "created_at": now, "updated_at": now,
			"presence_changed_at": now, "activity_changed_at": now,
			"observations": registry.Observations{Native: &registry.NativeObservation{
				Event: event, Activity: &idle, ObservedAt: now,
				Reporter: registry.Reporter{Integration: "codex-hook", Version: 1},
			}},
			"location": map[string]any{
				"kind": "tmux", "server_id": fixture.socket, "session_id": string(live.ID),
				"session_name": live.Name, "window_id": string(pane.WindowID),
				"pane_id": string(pane.ID), "pane_pid": pane.PID,
			},
		}
	}

	encoded, err := json.Marshal(map[string]any{"schema_version": 3, "revision": 1, "sessions": records})
	if err != nil {
		t.Fatal(err)
	}

	registryPath := filepath.Join(root, "idle-agents.json")
	if err := os.WriteFile(registryPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := session.SaveState(t.Context(), fixture.historyPath, &session.State{
		Version: session.CurrentHistoryVersion, PopupDisplayMode: "agents",
	}); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "picker.toml")
	if err := os.WriteFile(configPath, []byte("[agent_sessions]\npopup_sort = \"name\"\nstatus_style = \"text\"\n[display]\nagents_agent_format = \"{tree}{status_text} {agent_session} {harness}\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	alias := fmt.Sprintf("sesh-picker-initial-idle-%d-%s", os.Getpid(), filepath.Base(root))
	evidencePath := filepath.Join(os.TempDir(), alias)
	t.Logf("termtrace evidence %s", evidencePath)

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	record := exec.CommandContext(ctx, termtraceExecutable,
		"record", "-d", alias, "--out", evidencePath, "--cols", "100", "--rows", "12",
		"--until-screen", "idle-Stop", "--timeout", "8s", "--max-duration", "20s", "--no-input",
		executable, "-test.run=^TestPickerApplicationHelper$", "--", "pick", "--ui", "popup",
		"--config", configPath, testSocketFlag, fixture.socket,
		testRepositoryFlag, fixture.repository, "--lease-root", fixture.leaseRoot,
		"--history", fixture.historyPath, "--agent-state", registryPath,
	)

	record.Env = append(pickerEnvironmentWithout(os.Environ(), "NO_COLOR", "TERM"),
		"SESH_PICKER_HELPER=1", "TERM=xterm-256color")
	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	termtracePickerCommand(t, ctx, "wait", "-q", alias, "--idle=100ms", "--timeout", "5s")
	screen := pickerHoverScreen(t, ctx, alias)
	t.Logf("idle screen\n%s", strings.Join(screen, "\n"))
	assertIdleStartupAndCompletionStatuses(t, screen)
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--signal", "INT")
	termtracePickerCommand(t, ctx, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000")
}

func assertIdleStartupAndCompletionStatuses(t *testing.T, screen []string) {
	t.Helper()

	for _, event := range []string{"SessionStart", "Stop"} {
		found := false

		for _, row := range screen {
			if !strings.Contains(row, event) || !strings.Contains(row, "Codex") {
				continue
			}

			found = true

			if !strings.Contains(row, "IDLE") || strings.Contains(row, "IDLE*") != (event == "Stop") {
				t.Errorf("%s status row = %q", event, row)
			}
		}

		if !found {
			t.Errorf("missing %s agent row", event)
		}
	}
}
