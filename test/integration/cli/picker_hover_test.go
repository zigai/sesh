//go:build (linux || darwin) && integration

package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentsPopupHoverKeepsScrolledSessionRowsStable(t *testing.T) {
	requirePickerTermtrace(t)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	root := pickerIntegrationRoot(t)
	server := tmuxtest.NewServer(t)
	configPath, historyPath := prepareOrdinaryHoverSessions(t, root, server)

	executable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	alias := fmt.Sprintf("sesh-picker-hover-%d-%s", os.Getpid(), filepath.Base(root))
	evidencePath := filepath.Join(os.TempDir(), alias)
	t.Logf("termtrace evidence %s", evidencePath)

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()

	record := exec.CommandContext(ctx, termtraceExecutable,
		"record", "-d", alias, "--out", evidencePath, "--cols", "80", "--rows", "12",
		"--until-screen", "hover-session-00", "--timeout", "8s", "--max-duration", "20s", "--no-input",
		executable, "-test.run=^TestPickerApplicationHelper$", "--", "pick", "--ui", "popup",
		"--config", configPath, testSocketFlag, server.Endpoint().SocketPath,
		testRepositoryFlag, filepath.Join(root, "repository"), "--lease-root", filepath.Join(root, "leases"),
		"--history", historyPath, "--agent-state", filepath.Join(root, "agents.json"),
	)

	record.Env = append(pickerEnvironmentWithout(os.Environ(), "NO_COLOR", "TERM"),
		"SESH_PICKER_HELPER=1", "TERM=xterm-256color")

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	arguments := make([]string, 0, 21)
	arguments = append(arguments, "send", "-q", alias)

	for range 15 {
		arguments = append(arguments, "down")
	}

	arguments = append(arguments, "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, arguments...)

	before := pickerHoverScreen(t, ctx, alias)
	t.Logf("scrolled screen\n%s", strings.Join(before, "\n"))

	hoverRow, hoveredName := pickerHoverTarget(t, before)

	for motion := range 3 {
		termtracePickerCommand(t, ctx, "send", "-q", alias, "--text",
			fmt.Sprintf("\x1b[<35;5;%dM", hoverRow+1), "--until-idle=100ms", "--timeout", "5s")

		after := pickerHoverScreen(t, ctx, alias)
		assertPickerHoverRowsStable(t, motion+1, before, after)

		if !strings.Contains(after[hoverRow], "▌") || !strings.Contains(after[hoverRow], hoveredName) {
			t.Fatalf("hover motion %d did not select %q at row %d: %s", motion+1, hoveredName, hoverRow+1, after[hoverRow])
		}
	}

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--signal", "INT")
	termtracePickerCommand(t, ctx, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000")
}

func prepareOrdinaryHoverSessions(t *testing.T, root string, server *gotmux.Server) (string, string) {
	t.Helper()

	for index := range 20 {
		name := fmt.Sprintf("hover-session-%02d", index)
		if _, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name}); err != nil {
			t.Fatalf("create session %q: %v", name, err)
		}
	}

	historyPath := filepath.Join(root, "history.json")
	if err := session.SaveState(t.Context(), historyPath, &session.State{
		Version: session.CurrentHistoryVersion, PopupDisplayMode: "agents",
	}); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "picker.toml")
	if err := os.WriteFile(configPath, []byte("[agent_sessions]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return configPath, historyPath
}

func pickerHoverTarget(t *testing.T, before []string) (int, string) {
	t.Helper()

	hoverRow := -1

	for row, line := range before {
		if strings.Contains(line, "hover-session-") {
			hoverRow = row
			break
		}
	}

	if hoverRow < 0 || strings.Contains(before[hoverRow], "hover-session-00") {
		t.Fatalf("keyboard navigation did not scroll the sessions: %v", before)
	}

	_, hoveredName, _ := strings.Cut(before[hoverRow], "hover-session-")
	hoveredName = "hover-session-" + strings.Fields(hoveredName)[0]

	return hoverRow, hoveredName
}

func pickerHoverScreen(t *testing.T, ctx context.Context, alias string) []string {
	t.Helper()

	screen, err := exec.CommandContext(ctx, termtraceExecutable, "screen", "--format", "text", alias).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect hover screen: %v: %s", err, screen)
	}

	return strings.Split(strings.TrimSuffix(string(screen), "\n"), "\n")
}

func assertPickerHoverRowsStable(t *testing.T, motion int, before, after []string) {
	t.Helper()

	for row, line := range before {
		_, name, found := strings.Cut(line, "hover-session-")
		if !found {
			continue
		}

		name = "hover-session-" + strings.Fields(name)[0]
		if row >= len(after) || !strings.Contains(after[row], name) {
			t.Fatalf("hover motion %d moved session %q from row %d\nbefore\n%s\nafter\n%s",
				motion, name, row+1, strings.Join(before, "\n"), strings.Join(after, "\n"))
		}
	}
}
