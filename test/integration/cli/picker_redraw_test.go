//go:build (linux || darwin) && integration

package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestPickerSearchShrinkAndRegrowAvoidFullScreenErase(t *testing.T) {
	requirePickerTermtrace(t)

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	root := pickerIntegrationRoot(t)
	server := tmuxtest.NewServer(t)

	for _, name := range []string{"alpha-target", "bravo-stale", "charlie-stale"} {
		if _, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name}); err != nil {
			t.Fatalf("create session %q: %v", name, err)
		}
	}

	configPath := filepath.Join(root, "picker.toml")
	if err := os.WriteFile(configPath, []byte("[agent_sessions]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("write picker config: %v", err)
	}

	executable, err := pickerTestExecutable()
	if err != nil {
		t.Fatalf("resolve picker test executable: %v", err)
	}

	alias := "sesh-picker-redraw-" + strconv.Itoa(os.Getpid()) + "-" + filepath.Base(root)
	arguments := []string{
		"-test.run=^TestPickerApplicationHelper$", "--", "popup",
		"--config", configPath,
		testSocketFlag, server.Endpoint().SocketPath,
		testRepositoryFlag, filepath.Join(root, "repository"),
		"--lease-root", filepath.Join(root, "leases"),
		"--history", filepath.Join(root, "history.json"),
		"--agent-state", filepath.Join(root, "agents.json"),
	}

	ctx, cancel := context.WithTimeout(t.Context(), pickerTermtraceTimeout)
	defer cancel()

	recordArgs := make([]string, 0, 11+len(arguments))
	recordArgs = append(recordArgs,
		"record", "-d", alias,
		"--until-screen", "charlie-stale", "--timeout", "8s",
		"--max-duration", "12s", "--no-input", executable,
	)
	recordArgs = append(recordArgs, arguments...)
	record := exec.CommandContext(ctx, termtraceExecutable, recordArgs...)

	record.Env = append(pickerEnvironmentWithout(os.Environ(), "NO_COLOR", "TERM"),
		"SESH_PICKER_HELPER=1", "TERM=xterm-256color")
	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "before-shrink")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--text", "alpha-target", "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "after-shrink")
	assertPickerScreenRows(t, ctx, alias, []string{"alpha-target"}, []string{"bravo-stale", "charlie-stale"})

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "before-grow")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "ctrl+u", "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "after-grow")
	assertPickerScreenRows(t, ctx, alias,
		[]string{"alpha-target", "bravo-stale", "charlie-stale"}, nil)

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "before-empty")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--text", "zzzzmissing", "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "after-empty")
	assertPickerScreenRows(t, ctx, alias, []string{"No matches"},
		[]string{"alpha-target", "bravo-stale", "charlie-stale"})

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "before-regrow")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "ctrl+u", "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "after-regrow")
	assertPickerScreenRows(t, ctx, alias,
		[]string{"alpha-target", "bravo-stale", "charlie-stale"}, nil)
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "before-arrow")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "down", "--until-idle=100ms", "--timeout", "5s")
	termtracePickerCommand(t, ctx, "send", "-q", alias, "--mark", "after-arrow")

	termtracePickerCommand(t, ctx, "send", "-q", alias, "--signal", "INT")
	termtracePickerCommand(t, ctx, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000")

	shrinkOutput := termtracePickerOutputBetween(t, ctx, alias, "before-shrink", "after-shrink")
	assertPickerIncrementalRedraw(t, "shrinking search results", shrinkOutput)

	growOutput := termtracePickerOutputBetween(t, ctx, alias, "before-grow", "after-grow")
	assertPickerIncrementalRedraw(t, "growing search results", growOutput)

	emptyOutput := termtracePickerOutputBetween(t, ctx, alias, "before-empty", "after-empty")
	assertPickerIncrementalRedraw(t, "empty search results", emptyOutput)
	regrowOutput := termtracePickerOutputBetween(t, ctx, alias, "before-regrow", "after-regrow")
	assertPickerIncrementalRedraw(t, "regrowing empty search results", regrowOutput)

	arrowOutput := termtracePickerOutputBetween(t, ctx, alias, "before-arrow", "after-arrow")
	assertPickerIncrementalRedraw(t, "selection-only redraw", arrowOutput)
}

func assertPickerIncrementalRedraw(t *testing.T, operation string, output []byte) {
	t.Helper()

	if clearCount := bytes.Count(output, []byte("\x1b[2J")); clearCount != 0 {
		t.Fatalf("%s emitted %d full-screen erases: %q", operation, clearCount, output)
	}

	for _, sequence := range [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1049l")} {
		if count := bytes.Count(output, sequence); count != 0 {
			t.Fatalf("%s emitted %d alternate-screen transitions %q: %q", operation, count, sequence, output)
		}
	}
}

func termtracePickerCommand(t *testing.T, ctx context.Context, arguments ...string) {
	t.Helper()

	if output, err := exec.CommandContext(ctx, termtraceExecutable, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("termtrace %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func assertPickerScreenRows(t *testing.T, ctx context.Context, alias string, present, absent []string) {
	t.Helper()

	screen, err := exec.CommandContext(ctx, termtraceExecutable, "screen", alias).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect picker screen: %v: %s", err, screen)
	}

	for _, value := range present {
		if !bytes.Contains(screen, []byte(value)) {
			t.Fatalf("picker screen missing %q: %s", value, screen)
		}
	}

	for _, value := range absent {
		if bytes.Contains(screen, []byte(value)) {
			t.Fatalf("picker screen retained filtered row %q: %s", value, screen)
		}
	}
}

func termtracePickerOutputBetween(t *testing.T, ctx context.Context, alias, from, to string) []byte {
	t.Helper()

	output, err := exec.CommandContext(
		ctx, termtraceExecutable, "analyze", "output", "--raw",
		"--from-marker", from, "--to-marker", to, alias,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("read picker output between markers: %v: %s", err, output)
	}

	return output
}
