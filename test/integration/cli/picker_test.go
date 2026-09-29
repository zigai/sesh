//go:build (linux || darwin) && integration

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/cli"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	"github.com/zigai/sesh/internal/session"
)

const pickerTermtraceTimeout = 15 * time.Second

const pickerTerminalIntegrationRequired = "SESH_TERMINAL_INTEGRATION_REQUIRED"

const termtraceExecutable = "termtrace"

func TestPickerPopupClientDimensionsMatchAttachedTerminal(t *testing.T) {
	fixture := newCLISessionFixture(t)
	session := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, session)
	t.Setenv(pickerClientEnvironment, "")

	clientName := fixture.attachCLIClient(t, session.ID)

	clients, err := fixture.server.Clients(t.Context())
	if err != nil {
		t.Fatalf("list attached clients: %v", err)
	}

	var want gotmux.Size

	for _, client := range clients {
		if string(client.Name) == clientName {
			want = gotmux.Size{Width: client.Width, Height: client.Height}
			break
		}
	}

	if want.Width <= 0 || want.Height <= 0 {
		t.Fatalf("attached client %q has unusable size %+v", clientName, want)
	}

	client, err := cmdpicker.InvokingPickerClient(t.Context(), fixture.server)
	if err != nil {
		t.Fatalf("resolve popup client: %v", err)
	}

	got := gotmux.Size{Width: client.Width, Height: client.Height}
	if got != want {
		t.Fatalf("picker popup available size = %+v, want attached client size %+v", got, want)
	}
}

func TestPickerPresentationsRenderInCurrentTerminal(t *testing.T) {
	requirePickerTermtrace(t)

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TERM", "xterm-256color")

	root := pickerIntegrationRoot(t)

	server := tmuxtest.NewServer(t)
	if _, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "picker-visible"}); err != nil {
		t.Fatalf("create visible session: %v", err)
	}

	beforeSessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("list sessions before picker: %v", err)
	}

	configPath := filepath.Join(root, "picker.toml")
	if err := os.WriteFile(configPath, []byte("[agent_sessions]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("write picker config: %v", err)
	}

	testExecutable, err := pickerTestExecutable()
	if err != nil {
		t.Fatalf("resolve picker test executable: %v", err)
	}

	for _, test := range []struct {
		name      string
		ui        string
		noColor   bool
		wantColor bool
	}{
		{name: "fullscreen terminal profile", ui: "fullscreen", wantColor: true},
		{name: "fullscreen explicit no color", ui: "fullscreen", noColor: true, wantColor: false},
		{name: "bare popup terminal profile", ui: "popup", wantColor: true},
		{name: "bare popup explicit no color", ui: "popup", noColor: true, wantColor: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := []string{
				"pick", "--ui", test.ui, "--config", configPath,
				testSocketFlag, server.Endpoint().SocketPath,
				testRepositoryFlag, filepath.Join(root, "repository"),
				"--lease-root", filepath.Join(root, "leases"),
				"--history", filepath.Join(root, "history.json"),
				"--agent-state", filepath.Join(root, "agents.json"),
			}
			childArguments := append([]string{"-test.run=^TestPickerApplicationHelper$", "--"}, arguments...)
			childOutput := runPickerTermtrace(t, testExecutable, childArguments, test.ui, test.noColor)

			if got := pickerOutputHasColor(childOutput); got != test.wantColor {
				t.Fatalf("picker PTY color = %t, want %t: %q", got, test.wantColor, childOutput)
			}
		})
	}

	afterSessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("list sessions after picker: %v", err)
	}

	if !samePickerSessions(beforeSessions, afterSessions) {
		t.Fatalf("canceled picker mutated sessions: before=%v after=%v", beforeSessions, afterSessions)
	}
}

func runPickerTermtrace(t *testing.T, executable string, childArguments []string, presentation string, noColor bool) []byte {
	t.Helper()

	alias := "sesh-picker-" + strconv.Itoa(os.Getpid()) + "-" + presentation + "-color"
	if noColor {
		alias = "sesh-picker-" + strconv.Itoa(os.Getpid()) + "-" + presentation + "-no-color"
	}

	ctx, cancel := context.WithTimeout(t.Context(), pickerTermtraceTimeout)
	defer cancel()

	recordArgs := make([]string, 0, 11+len(childArguments))
	recordArgs = append(recordArgs,
		"record", "-d", alias,
		"--until-screen", "picker-visible", "--timeout", "8s",
		"--max-duration", "12s", "--no-input", executable,
	)
	recordArgs = append(recordArgs, childArguments...)
	record := exec.CommandContext(ctx, termtraceExecutable, recordArgs...)

	record.Env = append(pickerEnvironmentWithout(os.Environ(), "NO_COLOR", "TERM"),
		"SESH_PICKER_HELPER=1", "TERM=xterm-256color")
	if noColor {
		record.Env = append(record.Env, "NO_COLOR=1")
	}

	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	if output, err := exec.CommandContext(ctx, termtraceExecutable, "send", "-q", alias, "--signal", "INT").CombinedOutput(); err != nil {
		t.Fatalf("cancel picker application: %v: %s", err, output)
	}

	if output, err := exec.CommandContext(ctx, termtraceExecutable, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000").CombinedOutput(); err != nil {
		t.Fatalf("wait for canceled picker workers: %v: %s", err, output)
	}

	childOutput, err := exec.CommandContext(ctx, termtraceExecutable, "analyze", "output", "--raw", alias).CombinedOutput()
	if err != nil || !bytes.Contains(childOutput, []byte("terminal-restored")) {
		t.Fatalf("picker terminal restoration evidence: error=%v output=%q", err, childOutput)
	}

	return childOutput
}

func pickerEnvironmentWithout(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(names, name) {
			continue
		}

		filtered = append(filtered, entry)
	}

	return filtered
}

func pickerOutputHasColor(output []byte) bool {
	for _, sequence := range [][]byte{
		[]byte("\x1b[38;"), []byte("\x1b[38:"),
		[]byte("\x1b[48;"), []byte("\x1b[48:"),
	} {
		if bytes.Contains(output, sequence) {
			return true
		}
	}

	return false
}

func TestPickerApplicationHelper(t *testing.T) {
	if os.Getenv("SESH_PICKER_HELPER") != "1" {
		return
	}

	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		os.Exit(2)
	}

	before, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		os.Exit(3)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	runErr := cli.ExecuteApplication(ctx, os.Args[separator+1:], os.Stdin, os.Stdout, os.Stderr)

	stop()

	after, stateErr := term.GetState(os.Stdin.Fd())
	if stateErr != nil || !reflect.DeepEqual(before, after) {
		os.Exit(4)
	}

	_, _ = fmt.Fprint(os.Stdout, "terminal-restored")

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		os.Exit(cli.ExitCode(runErr))
	}

	os.Exit(0)
}

func failPickerTermtraceRecord(t *testing.T, ctx context.Context, alias string, output []byte, recordErr error) {
	t.Helper()

	_, _ = exec.CommandContext(ctx, termtraceExecutable, "send", "-q", alias, "--signal", "QUIT").CombinedOutput()
	_, _ = exec.CommandContext(ctx, termtraceExecutable, "status", "-q", alias, "--wait-complete", "--timeout-ms", "2000").CombinedOutput()
	diagnostic, _ := exec.CommandContext(ctx, termtraceExecutable, "analyze", alias).CombinedOutput()
	screen, _ := exec.CommandContext(ctx, termtraceExecutable, "screen", alias).CombinedOutput()
	childOutput, _ := exec.CommandContext(ctx, termtraceExecutable, "analyze", "output", "--strip-controls", alias).CombinedOutput()

	t.Fatalf("record picker until live session rendered: %v: %s\n%s\n%s\n%q", recordErr, output, diagnostic, screen, childOutput)
}

func pickerIntegrationRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("secure picker temp root: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))

	return root
}

func pickerTestExecutable() (string, error) {
	if runtime.GOOS == "darwin" {
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve Darwin test executable: %w", err)
		}

		return executable, nil
	}

	executable, err := filepath.EvalSymlinks("/proc/self/exe")
	if err != nil {
		return "", fmt.Errorf("resolve Linux test executable: %w", err)
	}

	return executable, nil
}

func samePickerSessions(left, right []gotmux.SessionInfo) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index].ID != right[index].ID || left[index].Name != right[index].Name {
			return false
		}
	}

	return true
}

func TestPickerClientSelectionUsesOnlyTheInvokingSession(t *testing.T) {
	fixture := newCLISessionFixture(t)
	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)
	t.Setenv(pickerClientEnvironment, "")

	first := fixture.attachCLIClient(t, origin.ID)
	assertInvokingPickerClient(t, fixture, first)

	second := fixture.attachCLIClient(t, origin.ID)
	if second == first {
		t.Fatal("fixture reused the first client instead of attaching a second")
	}

	if _, err := cmdpicker.InvokingPickerClient(t.Context(), fixture.server); !errors.Is(err, session.ErrInvokingClientUnavailable) {
		t.Fatalf("ambiguous clients = %v; want unavailable", err)
	}

	for _, name := range []string{first, second} {
		t.Setenv(pickerClientEnvironment, name)

		assertInvokingPickerClient(t, fixture, name)
	}

	other := fixture.newLiveSession(t, "other-client-session")

	outside := fixture.attachCLIClient(t, other.ID)
	for _, name := range []string{outside, "/dev/pts/not-a-client"} {
		t.Setenv(pickerClientEnvironment, name)

		if _, err := cmdpicker.InvokingPickerClient(t.Context(), fixture.server); !errors.Is(err, session.ErrInvokingClientUnavailable) {
			t.Fatalf("foreign or stale client hint = %v; want unavailable", err)
		}
	}

	for _, name := range []string{first, second} {
		if got := fixture.clientDisplayedSession(t, name); got != origin.ID {
			t.Fatalf("selection mutated client session: %s, want %s", got, origin.ID)
		}
	}

	if got := fixture.clientDisplayedSession(t, outside); got != other.ID {
		t.Fatalf("selection mutated unrelated client session: %s, want %s", got, other.ID)
	}
}

func assertInvokingPickerClient(t *testing.T, fixture *cliSessionFixture, name string) {
	t.Helper()

	client, err := cmdpicker.InvokingPickerClient(t.Context(), fixture.server)
	if err != nil || string(client.Name) != name {
		t.Fatalf("invoking client = %q, %v; want %q", client.Name, err, name)
	}
}
