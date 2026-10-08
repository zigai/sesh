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
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli"

	"github.com/charmbracelet/x/term"
	"github.com/zigai/gotmux/tmuxtest"
)

const snapshotPickerHelperEnvironment = "SESH_SNAPSHOT_PICKER_HELPER"

func TestSnapshotRestorePickerCleansTerminalBeforeRestoringSelectedGeneration(t *testing.T) {
	requirePickerTermtrace(t)

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	root := pickerIntegrationRoot(t)
	t.Setenv("HOME", filepath.Join(root, "home"))

	const (
		selectedName = "snapshot-picker-selected"
		currentName  = "snapshot-picker-current"
	)

	repository, leaseRoot, selectedGeneration, currentGeneration := captureSnapshotPickerTimeline(t, selectedName, currentName)
	target := tmuxtest.NewServer(t)

	testExecutable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	arguments := []string{
		"snapshot", "restore", "--pick", "--no-processes", "--no-config",
		"--repository", repository,
		"--lease-root", leaseRoot,
		"--socket", target.Endpoint().SocketPath,
	}
	childArguments := append([]string{"-test.run=^TestSnapshotPickerApplicationHelper$", "--"}, arguments...)
	alias := "sesh-snapshot-picker-" + strconv.Itoa(os.Getpid())

	ctx, cancel := context.WithTimeout(t.Context(), pickerTermtraceTimeout)
	defer cancel()

	recordArguments := make([]string, 0, 10+len(childArguments))
	recordArguments = append(recordArguments,
		"record", "-d", alias,
		"--until-screen", currentGeneration,
		"--timeout", "8s",
		"--max-duration", "12s",
		"--no-input",
		testExecutable,
	)
	recordArguments = append(recordArguments, childArguments...)

	record := exec.CommandContext(ctx, termtraceExecutable, recordArguments...)

	record.Env = append(os.Environ(), snapshotPickerHelperEnvironment+"=1")
	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	if output, err := exec.CommandContext(
		ctx,
		termtraceExecutable,
		"send", "-q", alias, "down", "enter", "--until-screen", "snapshot-picker-terminal-restored", "--timeout", "8s",
	).CombinedOutput(); err != nil {
		t.Fatalf("select snapshot generation: %v: %s", err, output)
	}

	if output, err := exec.CommandContext(ctx, termtraceExecutable, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000").CombinedOutput(); err != nil {
		t.Fatalf("wait for selected restore: %v: %s", err, output)
	}

	childOutput, err := exec.CommandContext(ctx, termtraceExecutable, "analyze", "output", "--raw", alias).CombinedOutput()
	if err != nil || !bytes.Contains(childOutput, []byte("snapshot-picker-terminal-restored")) {
		t.Fatalf("terminal restoration evidence: error=%v output=%q", err, childOutput)
	}

	if !pickerOutputHasColor(childOutput) {
		t.Fatal("snapshot picker did not retain its terminal colors")
	}

	if _, err := target.FindSession(t.Context(), selectedName); err != nil {
		t.Fatalf("selected generation was not restored: %v", err)
	}

	if _, err := target.FindSession(t.Context(), currentName); err == nil {
		t.Fatal("restore silently fell back to the current generation")
	}

	if bytes.Contains(childOutput, []byte("from generation "+currentGeneration[:8])) || !bytes.Contains(childOutput, []byte("from generation "+selectedGeneration[:8])) {
		t.Fatalf("restore receipt does not identify the exact selected generation: %q", childOutput)
	}
}

func TestSnapshotRestorePickerCancellationLeavesRepositoryAndTmuxUntouched(t *testing.T) {
	assertSnapshotRestorePickerCancellation(t, []string{"snapshot", "restore"}, "action")
}

func assertSnapshotRestorePickerCancellation(t *testing.T, command []string, aliasKind string) {
	t.Helper()

	requirePickerTermtrace(t)

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	root := pickerIntegrationRoot(t)
	t.Setenv("HOME", filepath.Join(root, "home"))

	repository, leaseRoot, generation := captureRemovedSession(t, "snapshot-picker-canceled")
	target := tmuxtest.NewServer(t)

	catalogBefore, err := os.ReadFile(filepath.Join(repository, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	sessionsBefore, err := target.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	testExecutable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	arguments := slices.Clone(command)
	arguments = append(arguments,
		"--pick", "--no-processes", "--no-config",
		"--repository", repository,
		"--lease-root", leaseRoot,
		"--socket", target.Endpoint().SocketPath,
	)
	childArguments := append([]string{"-test.run=^TestSnapshotPickerApplicationHelper$", "--"}, arguments...)
	alias := "sesh-snapshot-picker-cancel-" + aliasKind + "-" + strconv.Itoa(os.Getpid())

	ctx, cancel := context.WithTimeout(t.Context(), pickerTermtraceTimeout)
	defer cancel()

	recordArguments := make([]string, 0, 10+len(childArguments))
	recordArguments = append(recordArguments,
		"record", "-d", alias,
		"--until-screen", generation,
		"--timeout", "8s",
		"--max-duration", "12s",
		"--no-input",
		testExecutable,
	)
	recordArguments = append(recordArguments, childArguments...)

	record := exec.CommandContext(ctx, termtraceExecutable, recordArguments...)

	record.Env = append(os.Environ(), snapshotPickerHelperEnvironment+"=1")
	if output, err := record.CombinedOutput(); err != nil {
		failPickerTermtraceRecord(t, ctx, alias, output, err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	if output, err := exec.CommandContext(
		ctx,
		termtraceExecutable,
		"send", "-q", alias, "q", "--until-screen", "snapshot-picker-terminal-restored", "--timeout", "8s",
	).CombinedOutput(); err != nil {
		t.Fatalf("cancel snapshot selection: %v: %s", err, output)
	}

	if output, err := exec.CommandContext(ctx, termtraceExecutable, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000").CombinedOutput(); err != nil {
		t.Fatalf("wait for canceled selection: %v: %s", err, output)
	}

	catalogAfter, err := os.ReadFile(filepath.Join(repository, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(catalogBefore, catalogAfter) {
		t.Fatal("canceled picker changed the repository catalog")
	}

	sessionsAfter, err := target.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !samePickerSessions(sessionsBefore, sessionsAfter) {
		t.Fatalf("canceled picker changed tmux: before=%v after=%v", sessionsBefore, sessionsAfter)
	}
}

func captureSnapshotPickerTimeline(t *testing.T, selectedName, currentName string) (string, string, string, string) {
	t.Helper()

	source := newCaptureCLIServer(t)

	sessions, err := source.Sessions(t.Context())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("source fixture: %v", err)
	}

	handle := sessions[0].Handle()
	if err := handle.Rename(t.Context(), selectedName); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(t.TempDir(), "repository")
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	save := func() string {
		result := executeJSON(
			t,
			"snapshot", "save",
			testRepositoryFlag, repository,
			testLeaseRootFlag, leaseRoot,
			testSocketFlag, source.Endpoint().SocketPath,
			string(sessions[0].ID),
			testNoConfigFlag,
			testJSONFlag,
		)
		if result.err != nil || !result.envelope.OK {
			t.Fatalf("save timeline generation: %v / %s", result.err, result.stderr)
		}

		return decodeJSONPayload[snapshotSaveResultJSON](t, result.envelope.Result).Generation
	}

	selectedGeneration := save()

	if err := handle.Rename(t.Context(), currentName); err != nil {
		t.Fatal(err)
	}

	currentGeneration := save()
	if selectedGeneration == currentGeneration {
		t.Fatal("timeline generations are not immutable")
	}

	if err := handle.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	return repository, leaseRoot, selectedGeneration, currentGeneration
}

func TestSnapshotPickerApplicationHelper(t *testing.T) {
	if os.Getenv(snapshotPickerHelperEnvironment) != "1" {
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

	_, _ = fmt.Fprint(os.Stdout, "snapshot-picker-terminal-restored")

	if runErr != nil && !errors.Is(runErr, app.ErrPartialRestore) {
		os.Exit(ExitCode(runErr))
	}

	os.Exit(0)
}
