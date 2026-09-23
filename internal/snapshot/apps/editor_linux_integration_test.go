//go:build linux && integration

package apps_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	process "github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestEnrichEditorReadsListedFilesFromOwnedNvimServer(t *testing.T) {
	nvim := requireFixture(t, "nvim")

	tests := []struct {
		name           string
		explicitListen bool
	}{
		{name: "explicit listen socket", explicitListen: true},
		{name: "default owned socket", explicitListen: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testEnrichEditorOwnedServer(t, nvim, test.explicitListen)
		})
	}
}

func testEnrichEditorOwnedServer(t *testing.T, nvim string, explicitListen bool) {
	t.Helper()

	directory := t.TempDir()
	first := filepath.Join(directory, "first file.txt")
	second := filepath.Join(directory, "second.txt")
	command := startNvim(t, nvim, directory, explicitListen, first, second)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	birth, err := process.ReadIdentity(ctx, command.Process.Pid)
	if err != nil {
		t.Fatalf("reading nvim identity: %v", err)
	}

	observation, err := observeProcess(ctx, command.Process.Pid, birth)
	if err != nil {
		t.Fatalf("observing nvim: %v", err)
	}

	prepared, reason, err := waitForEditorEnrichment(ctx, observedLaunch(observation), command.Process.Pid)
	if err != nil {
		t.Fatalf("EnrichEditor did not find owned nvim endpoint: %v", err)
	}

	want := []string{"nvim", "--", first, second}
	if reason != apps.ReasonEditorReopen || !slices.Equal(byteArguments(prepared.Arguments), want) ||
		prepared.Recovery != apps.RecoveryRestart {
		t.Fatalf("enriched launch = %#v, reason %q; want argv %#v", prepared, reason, want)
	}
}

func TestEnrichEditorRestartsNvimWithoutListedFiles(t *testing.T) {
	nvim := requireFixture(t, "nvim")

	directory := t.TempDir()
	command := startNvim(t, nvim, directory, false)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	birth, err := process.ReadIdentity(ctx, command.Process.Pid)
	if err != nil {
		t.Fatalf("reading nvim identity: %v", err)
	}

	observation, err := observeProcess(ctx, command.Process.Pid, birth)
	if err != nil {
		t.Fatalf("observing nvim: %v", err)
	}

	prepared, reason, err := waitForEditorEnrichment(ctx, observedLaunch(observation), command.Process.Pid)
	if err != nil {
		t.Fatalf("EnrichEditor failed on empty nvim: %v", err)
	}

	want := []string{"nvim"}
	if reason != apps.ReasonEditorRestart || !slices.Equal(byteArguments(prepared.Arguments), want) ||
		prepared.Recovery != apps.RecoveryRestart {
		t.Fatalf("enriched launch = %#v, reason %q; want argv %#v", prepared, reason, want)
	}
}

func TestEnrichEditorExcludesUnlistedAndScratchBuffers(t *testing.T) {
	nvim := requireFixture(t, "nvim")

	directory := t.TempDir()
	listed := filepath.Join(directory, "listed.txt")
	command := startNvimWithExtraArgs(t, nvim, directory, []string{"-c", "enew | setlocal buftype=nofile"}, listed)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	birth, err := process.ReadIdentity(ctx, command.Process.Pid)
	if err != nil {
		t.Fatalf("reading nvim identity: %v", err)
	}

	observation, err := observeProcess(ctx, command.Process.Pid, birth)
	if err != nil {
		t.Fatalf("observing nvim: %v", err)
	}

	prepared, reason, err := waitForEditorEnrichment(ctx, observedLaunch(observation), command.Process.Pid)
	if err != nil {
		t.Fatalf("EnrichEditor failed: %v", err)
	}

	want := []string{"nvim", "--", listed}
	if reason != apps.ReasonEditorReopen || !slices.Equal(byteArguments(prepared.Arguments), want) ||
		prepared.Recovery != apps.RecoveryRestart {
		t.Fatalf("enriched launch = %#v, reason %q; want argv %#v", prepared, reason, want)
	}
}

func TestEnrichEditorRejectsIdentityMismatch(t *testing.T) {
	nvim := requireFixture(t, "nvim")

	directory := t.TempDir()
	first := filepath.Join(directory, "file.txt")
	command := startNvim(t, nvim, directory, false, first)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	birth, err := process.ReadIdentity(ctx, command.Process.Pid)
	if err != nil {
		t.Fatalf("reading nvim identity: %v", err)
	}

	observation, err := observeProcess(ctx, command.Process.Pid, birth)
	if err != nil {
		t.Fatalf("observing nvim: %v", err)
	}

	launch := observedLaunch(observation)
	launch.Birth = model.Bytes("999999:999999")

	_, reason, err := apps.EnrichEditor(ctx, launch, command.Process.Pid)
	if err == nil || !errors.Is(err, apps.ErrEditorIdentityChanged) || reason != apps.ReasonEditorIdentityChanged {
		t.Fatalf("EnrichEditor accepted forged birth identity: reason %q, err %v", reason, err)
	}
}

func TestEnrichEditorReadsFilesOpenedAfterInteractiveNvimStartup(t *testing.T) {
	nvim := requireFixture(t, "nvim")
	directory := t.TempDir()
	first := filepath.Join(directory, "first.txt")
	second := filepath.Join(directory, "second-after-start.txt")

	writeEditorFile(t, first, "first\n")
	writeEditorFile(t, second, "second\n")

	interactive := startInteractiveNvim(t, nvim, directory, first)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	birth, err := process.ReadIdentity(ctx, interactive.command.Process.Pid)
	if err != nil {
		t.Fatalf("reading interactive nvim identity: %v", err)
	}

	observation, err := observeProcess(ctx, interactive.command.Process.Pid, birth)
	if err != nil {
		t.Fatalf("observing interactive nvim: %v", err)
	}

	launch := observedLaunch(observation)

	prepared, reason, err := waitForEditorArguments(
		ctx, launch, interactive.command.Process.Pid, []string{"nvim", "--", first},
	)
	if err != nil {
		t.Fatalf("reading initial interactive nvim buffers: %v", err)
	}

	if reason != apps.ReasonEditorReopen || prepared.Recovery != apps.RecoveryRestart {
		t.Fatalf("initial interactive enrichment reason/recovery = %q/%q", reason, prepared.Recovery)
	}

	if _, err := interactive.master.WriteString(":edit " + second + "\r"); err != nil {
		t.Fatalf("opening second file through interactive nvim PTY: %v", err)
	}

	want := []string{"nvim", "--", first, second}

	prepared, reason, err = waitForEditorArguments(ctx, launch, interactive.command.Process.Pid, want)
	if err != nil {
		t.Fatalf("reading buffers opened after interactive nvim startup: %v", err)
	}

	if reason != apps.ReasonEditorReopen || prepared.Recovery != apps.RecoveryRestart {
		t.Fatalf("updated interactive enrichment reason/recovery = %q/%q", reason, prepared.Recovery)
	}

	if _, err := os.Lstat(filepath.Join(directory, ".config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("editor query wrote configuration state into observed directory: %v", err)
	}

	assertInteractiveEditorIdentityRefusals(
		t, ctx, nvim, directory, first, launch, interactive.command.Process.Pid,
	)
	interactive.stop(t, ctx)
}

type interactiveNvim struct {
	command *exec.Cmd
	master  *os.File
	wait    chan error
	stopped bool
}

func writeEditorFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing editor fixture: %v", err)
	}
}

func startInteractiveNvim(t *testing.T, nvim, directory, first string) *interactiveNvim {
	t.Helper()

	master, slave := openTestPTY(t)
	command := exec.CommandContext(t.Context(), nvim, "--clean", first)
	command.Dir = directory
	command.Env = isolatedEditorEnvironment(t, directory)
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	if err := command.Start(); err != nil {
		t.Fatalf("starting interactive nvim in PTY: %v", err)
	}

	interactive := &interactiveNvim{command: command, master: master, wait: make(chan error, 1), stopped: false}
	go func() {
		interactive.wait <- command.Wait()
	}()

	t.Cleanup(func() {
		interactive.cleanup(t)
	})

	return interactive
}

func (nvim *interactiveNvim) cleanup(t *testing.T) {
	t.Helper()

	if nvim.stopped {
		return
	}

	killErr := nvim.command.Process.Kill()
	waitErr := <-nvim.wait

	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		t.Errorf("killing interactive nvim during cleanup: %v", killErr)
	}

	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		t.Errorf("waiting for interactive nvim during cleanup: %v", waitErr)
	}
}

func (nvim *interactiveNvim) stop(t *testing.T, ctx context.Context) {
	t.Helper()

	if _, err := nvim.master.WriteString("\x1b:qa!\r"); err != nil {
		t.Fatalf("quitting interactive nvim through PTY: %v", err)
	}

	select {
	case waitErr := <-nvim.wait:
		if waitErr != nil {
			t.Fatalf("interactive nvim failed to quit cleanly: %v", waitErr)
		}

		nvim.stopped = true
	case <-ctx.Done():
		t.Fatalf("interactive nvim did not quit before deadline: %v", ctx.Err())
	}
}

func assertInteractiveEditorIdentityRefusals(
	t *testing.T,
	ctx context.Context,
	nvim string,
	directory string,
	first string,
	launch model.Launch,
	interactivePID int,
) {
	t.Helper()

	replaced := launch
	replaced.Birth = model.Bytes("999999:999999")

	_, replacedReason, replacedErr := apps.EnrichEditor(ctx, replaced, interactivePID)
	if !errors.Is(replacedErr, apps.ErrEditorIdentityChanged) || replacedReason != apps.ReasonEditorIdentityChanged {
		t.Fatalf("interactive nvim replacement accepted: reason %q, err %v", replacedReason, replacedErr)
	}

	unrelated := startNvimContext(t, ctx, nvim, directory, true, first)

	unrelatedBirth, err := process.ReadIdentity(ctx, unrelated.Process.Pid)
	if err != nil {
		t.Fatalf("reading unrelated nvim identity: %v", err)
	}

	unrelatedObservation, err := observeProcess(ctx, unrelated.Process.Pid, unrelatedBirth)
	if err != nil {
		t.Fatalf("observing unrelated nvim: %v", err)
	}

	unrelatedLaunch := observedLaunch(unrelatedObservation)
	if _, _, err := waitForEditorEnrichment(ctx, unrelatedLaunch, unrelated.Process.Pid); err != nil {
		t.Fatalf("waiting for unrelated nvim endpoint: %v", err)
	}

	_, unrelatedReason, unrelatedErr := apps.EnrichEditor(ctx, unrelatedLaunch, interactivePID)
	if !errors.Is(unrelatedErr, apps.ErrEditorIdentityChanged) || unrelatedReason != apps.ReasonEditorIdentityChanged {
		t.Fatalf("unrelated nvim endpoint accepted for interactive process: reason %q, err %v", unrelatedReason, unrelatedErr)
	}
}

func TestApplicationIntegrationRequiredFixtures(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{"nvim", topName, "htop", "bash", "node"} {
		t.Run(fixture, func(t *testing.T) {
			path := requireFixture(t, fixture)
			if !filepath.IsAbs(path) {
				t.Fatalf("fixture path %q is not absolute", path)
			}
		})
	}
}

var errFixtureOptionalSkip = errors.New("fixture unavailable (optional skip)")

func resolveFixture(binary string, required bool) (string, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		if required {
			return "", fmt.Errorf("required fixture %q is unavailable: %w", binary, err)
		}

		return "", errFixtureOptionalSkip
	}

	return path, nil
}

func requireFixture(t *testing.T, binary string) string {
	t.Helper()

	required := os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") == "1"

	path, err := resolveFixture(binary, required)
	if err != nil {
		if errors.Is(err, errFixtureOptionalSkip) {
			t.Skipf("optional application integration fixture %q is unavailable", binary)
		}

		t.Fatalf("application integration fixture failure: %v", err)
	}

	return path
}

func TestResolveFixtureGateBehavior(t *testing.T) {
	t.Parallel()

	_, err := resolveFixture("nonexistent-fixture-xyz-999", false)
	if !errors.Is(err, errFixtureOptionalSkip) {
		t.Fatalf("resolveFixture(optional) = %v, want errFixtureOptionalSkip", err)
	}

	_, err = resolveFixture("nonexistent-fixture-xyz-999", true)
	if err == nil || errors.Is(err, errFixtureOptionalSkip) {
		t.Fatalf("resolveFixture(required) = %v, want fatal error", err)
	}
}

func isolatedEditorEnvironment(t *testing.T, directory string) []string {
	t.Helper()

	home := filepath.Join(directory, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("creating isolated home: %v", err)
	}

	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"TERM=xterm-256color",
		"LC_ALL=C",
		"LANG=C",
		"PATH=" + os.Getenv("PATH"),
	}
}

func startNvimWithExtraArgs(t *testing.T, nvim, directory string, extraArgs []string, files ...string) *exec.Cmd {
	t.Helper()

	arguments := make([]string, 0, 2+len(extraArgs)+len(files))
	arguments = append(arguments, "--clean", "--headless")
	arguments = append(arguments, extraArgs...)
	arguments = append(arguments, files...)
	command := exec.CommandContext(t.Context(), nvim, arguments...)
	command.Dir = directory
	command.Env = isolatedEditorEnvironment(t, directory)
	command.Stdout = io.Discard
	command.Stderr = io.Discard

	if err := command.Start(); err != nil {
		t.Fatalf("starting isolated nvim: %v", err)
	}

	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	return command
}

func startNvim(t *testing.T, nvim, directory string, explicitListen bool, files ...string) *exec.Cmd {
	t.Helper()

	return startNvimContext(t, t.Context(), nvim, directory, explicitListen, files...)
}

func startNvimContext(
	t *testing.T,
	ctx context.Context,
	nvim string,
	directory string,
	explicitListen bool,
	files ...string,
) *exec.Cmd {
	t.Helper()

	arguments := []string{"--clean", "--headless"}
	if explicitListen {
		arguments = append(arguments, "--listen", filepath.Join(directory, "nvim.explicit.sock"))
	}

	arguments = append(arguments, files...)
	command := exec.CommandContext(ctx, nvim, arguments...)
	command.Dir = directory
	command.Env = isolatedEditorEnvironment(t, directory)
	command.Stdout = io.Discard
	command.Stderr = io.Discard

	if err := command.Start(); err != nil {
		t.Fatalf("starting isolated nvim: %v", err)
	}

	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	return command
}

func observeProcess(ctx context.Context, pid int, birth process.Identity) (*process.Observation, error) {
	for ctx.Err() == nil {
		observation, err := process.Observe(ctx, process.Request{PID: pid, ExpectedBirth: birth, EnvironmentKeys: nil})
		if err == nil {
			return observation, nil
		}

		if !errors.Is(err, process.ErrEmptyCmdline) {
			return nil, fmt.Errorf("observing process: %w", err)
		}

		time.Sleep(10 * time.Millisecond)
	}

	return nil, fmt.Errorf("observing process: %w", ctx.Err())
}

func waitForEditorEnrichment(
	ctx context.Context,
	observed model.Launch,
	pid int,
) (model.Launch, apps.Reason, error) {
	for ctx.Err() == nil {
		prepared, reason, err := apps.EnrichEditor(ctx, observed, pid)
		if err == nil || !errors.Is(err, apps.ErrEditorEndpointMissing) {
			if err != nil {
				return prepared, reason, fmt.Errorf("enrich editor: %w", err)
			}

			return prepared, reason, nil
		}

		time.Sleep(20 * time.Millisecond)
	}

	return model.Launch{}, "", fmt.Errorf("wait for editor enrichment: %w", ctx.Err())
}

func waitForEditorArguments(
	ctx context.Context,
	observed model.Launch,
	pid int,
	want []string,
) (model.Launch, apps.Reason, error) {
	for ctx.Err() == nil {
		prepared, reason, err := apps.EnrichEditor(ctx, observed, pid)
		if err == nil && slices.Equal(byteArguments(prepared.Arguments), want) {
			return prepared, reason, nil
		}

		if err != nil && !errors.Is(err, apps.ErrEditorEndpointMissing) {
			return prepared, reason, fmt.Errorf("enrich editor: %w", err)
		}

		time.Sleep(20 * time.Millisecond)
	}

	return model.Launch{}, "", fmt.Errorf("wait for editor arguments: %w", ctx.Err())
}

func observedLaunch(observation *process.Observation) model.Launch {
	arguments := make([]model.Bytes, len(observation.Arguments))
	for index, argument := range observation.Arguments {
		arguments[index] = slices.Clone(argument)
	}

	return model.Launch{
		Executable: slices.Clone(observation.Executable), Arguments: arguments,
		Directory: slices.Clone(observation.Directory), Environment: nil,
		Birth: model.Bytes(observation.Birth.Bytes()), Recovery: apps.RecoveryUnverified,
		Reason: "",
	}
}

func byteArguments(arguments []model.Bytes) []string {
	result := make([]string, len(arguments))
	for index, argument := range arguments {
		result[index] = string(argument)
	}

	return result
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	failOrSkip := func(msg string, err error) {
		if os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s in required mode: %v", msg, err)
		}

		t.Skipf("%s: %v", msg, err)
	}

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		failOrSkip("pty allocation unavailable", err)

		return nil, nil
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()

		failOrSkip("pty unlock failed", err)

		return nil, nil
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()

		failOrSkip("pty get number failed", err)

		return nil, nil
	}

	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()

		failOrSkip("pty open slave failed", err)

		return nil, nil
	}

	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})

	return master, slave
}

func TestMonitorAcceptedArgumentsStartInRealPTY(t *testing.T) {
	topPath := requireFixture(t, topName)

	t.Run("top accepted arguments start successfully in real pty", func(t *testing.T) {
		testTopAcceptedArgumentsStartInRealPTY(t, topPath)
	})

	t.Run("top rejected argument exits with error in real pty", func(t *testing.T) {
		testTopRejectedArgumentExitsWithErrorInRealPTY(t, topPath)
	})

	t.Run("htop accepted sort key starts successfully in real pty", func(t *testing.T) {
		testHtopAcceptedSortKeyStartsSuccessfullyInRealPTY(t)
	})
}

func readRunningTopHeader(master *os.File) (int, bool) {
	var buf [4096]byte

	_ = master.SetReadDeadline(time.Now().Add(2 * time.Second))
	totalRead := 0

	for totalRead < len(buf) {
		n, err := master.Read(buf[totalRead:])
		if n > 0 {
			totalRead += n
			if isRunningTopOutput(buf[:totalRead]) {
				return totalRead, true
			}
		}

		if err != nil {
			break
		}
	}

	return totalRead, isRunningTopOutput(buf[:totalRead])
}

func isRunningTopOutput(data []byte) bool {
	return bytes.Contains(data, []byte("top - ")) ||
		bytes.Contains(data, []byte("Tasks:")) ||
		bytes.Contains(data, []byte("%Cpu"))
}

func testTopAcceptedArgumentsStartInRealPTY(t *testing.T, topPath string) {
	t.Helper()

	tempDir := t.TempDir()
	observed := model.Launch{
		Executable: model.Bytes(topPath),
		Arguments: []model.Bytes{
			model.Bytes(topName),
			model.Bytes("-d"), model.Bytes("1.5"),
			model.Bytes("-c"),
			model.Bytes("-o"), model.Bytes("%CPU"),
		},
		Directory:   model.Bytes(tempDir),
		Environment: nil,
		Birth:       nil,
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}

	prepared, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonMonitorRestart || !reason.Supported() {
		t.Fatalf("Prepare() reason = %q, want ReasonMonitorRestart", reason)
	}

	argv := byteArguments(prepared.Arguments)
	master, slave := openTestPTY(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, string(prepared.Executable), argv[1:]...)
	cmd.Dir = tempDir
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = isolatedEditorEnvironment(t, tempDir)

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting top in PTY: %v", err)
	}

	totalRead, hasHeader := readRunningTopHeader(master)
	if !hasHeader {
		t.Fatalf("expected recognizable running top header in PTY output, read %d bytes", totalRead)
	}

	if _, err := master.WriteString("q"); err != nil {
		t.Fatalf("writing quit to PTY: %v", err)
	}

	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		t.Fatalf("top process timed out before quitting cleanly: %v", ctx.Err())
	}

	if waitErr != nil {
		t.Fatalf("top failed to quit cleanly: %v", waitErr)
	}

	if !cmd.ProcessState.Success() {
		t.Fatalf("top exited with non-zero exit code: %d", cmd.ProcessState.ExitCode())
	}
}

func testTopRejectedArgumentExitsWithErrorInRealPTY(t *testing.T, topPath string) {
	t.Helper()

	tempDir := t.TempDir()
	observed := model.Launch{
		Executable: model.Bytes(topPath),
		Arguments: []model.Bytes{
			model.Bytes(topName),
			model.Bytes("-o"), model.Bytes("cpu"),
		},
		Directory:   model.Bytes(tempDir),
		Environment: nil,
		Birth:       nil,
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}

	_, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() {
		t.Fatalf("Prepare() reason = %q, want ReasonUnsafeArguments", reason)
	}

	master, slave := openTestPTY(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

	defer cancel()

	cmd := exec.CommandContext(ctx, topPath, "-o", "cpu")
	cmd.Dir = tempDir
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = isolatedEditorEnvironment(t, tempDir)

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	err := cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("top -o cpu timed out rather than failing immediately: %v", ctx.Err())
	}

	if err == nil {
		t.Fatal("expected top -o cpu to fail with non-zero exit code, got nil")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got: %T (%v)", err, err)
	}

	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got: %d", exitErr.ExitCode())
	}

	var buf [512]byte

	_ = master.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

	n, _ := master.Read(buf[:])
	outStr := string(buf[:n])

	if !strings.Contains(outStr, "unrecognized field name") {
		t.Fatalf("expected unrecognized field error in output, read %d bytes", n)
	}
}

func testHtopAcceptedSortKeyStartsSuccessfullyInRealPTY(t *testing.T) {
	t.Helper()

	htopPath := requireFixture(t, "htop")

	tempDir := t.TempDir()
	observed := model.Launch{
		Executable: model.Bytes(htopPath),
		Arguments: []model.Bytes{
			model.Bytes("htop"),
			model.Bytes("-d"), model.Bytes("10"),
			model.Bytes("-C"),
			model.Bytes("-s"), model.Bytes("PID"),
		},
		Directory:   model.Bytes(tempDir),
		Environment: nil,
		Birth:       nil,
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}

	prepared, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonMonitorRestart || !reason.Supported() {
		t.Fatalf("Prepare() reason = %q, want ReasonMonitorRestart", reason)
	}

	argv := byteArguments(prepared.Arguments)
	master, slave := openTestPTY(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, string(prepared.Executable), argv[1:]...)
	cmd.Dir = tempDir
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = isolatedEditorEnvironment(t, tempDir)

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting htop in PTY: %v", err)
	}

	var buf [512]byte

	_ = master.SetReadDeadline(time.Now().Add(2 * time.Second))

	n, readErr := master.Read(buf[:])
	if n == 0 && readErr != nil {
		t.Fatalf("reading htop PTY output: %v", readErr)
	}

	if _, err := master.WriteString("q"); err != nil {
		t.Fatalf("writing quit to PTY: %v", err)
	}

	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		t.Fatalf("htop timed out: %v", ctx.Err())
	}

	if waitErr != nil {
		t.Fatalf("htop failed to quit cleanly: %v", waitErr)
	}

	if !cmd.ProcessState.Success() {
		t.Fatalf("htop exited with non-zero exit code: %d", cmd.ProcessState.ExitCode())
	}
}
