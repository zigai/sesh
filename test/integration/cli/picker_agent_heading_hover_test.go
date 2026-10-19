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

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentsPopupHoverKeepsVisibleSessionHeadingInPlace(t *testing.T) {
	requirePickerTermtrace(t)
	clearAmbientTmux(t)

	root := pickerIntegrationRoot(t)
	fixture := newCLISessionFixture(t)

	registryPath := prepareHeadingHoverRegistry(t, fixture, root)
	if err := session.SaveState(t.Context(), fixture.historyPath, &session.State{
		Version: session.CurrentHistoryVersion, PopupDisplayMode: "agents",
	}); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "picker.toml")
	if err := os.WriteFile(configPath, []byte("[agent_sessions]\npopup_sort = \"name\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	alias := fmt.Sprintf("sesh-picker-heading-hover-%d-%s", os.Getpid(), filepath.Base(root))
	evidencePath := filepath.Join(t.ArtifactDir(), "termtrace")
	t.Logf("termtrace evidence %s", evidencePath)

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	record := exec.CommandContext(ctx, termtraceExecutable,
		"record", "-d", alias, "--out", evidencePath, "--cols", "80", "--rows", "9",
		"--until-screen", "heading-session-bravo", "--timeout", "8s", "--max-duration", "20s", "--no-input",
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
	before := pickerHoverScreen(t, ctx, alias)
	t.Logf("heading screen\n%s", strings.Join(before, "\n"))
	assertHeadingHoverRows(t, before)

	for motion := range 3 {
		termtracePickerCommand(t, ctx, "send", "-q", alias, "--text", "\x1b[<35;5;7M",
			"--until-idle=100ms", "--timeout", "5s")

		after := pickerHoverScreen(t, ctx, alias)
		assertHeadingHoverRows(t, after)

		if !strings.Contains(after[6], "▌") {
			t.Fatalf("hover motion %d did not select the visible session heading: %s", motion+1, after[6])
		}
	}

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--signal", "INT")
	termtracePickerCommand(t, ctx, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000")
}

func assertHeadingHoverRows(t *testing.T, screen []string) {
	t.Helper()

	for index, name := range []string{
		"heading-session-alpha", "a0 Omp", "a1 Omp", "a2 Omp", "heading-session-bravo",
	} {
		row := index + 2
		if row >= len(screen) || !strings.Contains(strings.Join(strings.Fields(screen[row]), " "), name) {
			t.Fatalf("expected %q at row %d\n%s", name, row+1, strings.Join(screen, "\n"))
		}
	}
}

func prepareHeadingHoverRegistry(t *testing.T, fixture *cliSessionFixture, root string) string {
	t.Helper()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	records := make(map[string]any, 9)

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		live := fixture.newLiveSession(t, "heading-session-"+name)

		panes, err := live.Handle().Panes(t.Context())
		if err != nil || len(panes) != 1 {
			t.Fatalf("heading session panes: %v", err)
		}

		for range 2 {
			if _, err := panes[0].Handle().Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionVertical}); err != nil {
				t.Fatal(err)
			}
		}

		panes, err = live.Handle().Panes(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		for index, pane := range panes {
			id := fmt.Sprintf("%s%d", name[:1], index)
			records[id] = map[string]any{
				"schema_version": 3, "id": id, "harness": "omp", "liveness": map[string]any{"kind": "live", "activity": "running"},
				"session_id": id, "created_at": now, "updated_at": now,
				"presence_changed_at": now, "activity_changed_at": now,
				"location": map[string]any{
					"kind": "tmux", "server_id": fixture.socket, "session_id": string(live.ID),
					"session_name": live.Name, "window_id": string(pane.WindowID),
					"pane_id": string(pane.ID), "pane_pid": pane.PID,
				},
			}
		}
	}

	encoded, err := json.Marshal(map[string]any{"schema_version": 3, "revision": 1, "sessions": records})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "heading-agents.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
