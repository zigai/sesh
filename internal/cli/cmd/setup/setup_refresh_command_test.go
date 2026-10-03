package setup_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/setup"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const fakeSystemctlFailureExit = 3

func TestSetupServicesShowsStartCommandForSelectedFeatures(t *testing.T) {
	for _, feature := range []string{"autosave", "coordinator", "managed-tmux"} {
		t.Run(feature, func(t *testing.T) {
			setupRefreshHome(t)
			fake := setupRefreshManager(t, "")
			args := []string{"setup", "services", "--apply", testNoConfigFlag}
			want := "systemctl --user enable --now sesh-autosave.timer"

			switch feature {
			case "coordinator":
				args = append(args, "--with-coordinator")
				want += " sesh-coordinator.service"
			case "managed-tmux":
				args = append(args, "--with-tmux-service", "--tmux", createFakeTmuxExecutable(t))
				want += " sesh-tmux.service"
			}

			stdout, stderr, err := executeCommand(t, args...)
			if err != nil || stderr != "" || !strings.Contains(stdout, "To start automatic saves:\n  "+want+"\n") {
				t.Fatalf("setup did not explain how to start the selected features: stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}

			if !slices.Equal(fake.commands(t), []string{"daemon-reload"}) {
				t.Fatalf("setup must leave enabling and starting to the user: %v", fake.commands(t))
			}
		})
	}
}

func TestSetupServicesDoesNotSuggestStartingAfterFailedRefresh(t *testing.T) {
	setupRefreshHome(t)
	setupRefreshManager(t, "daemon-reload")

	stdout, _, err := executeCommand(t, "setup", "services", "--apply", testNoConfigFlag)
	if err == nil || strings.Contains(stdout, "To start automatic saves:") {
		t.Fatalf("failed setup must not suggest starting: stdout=%q err=%v", stdout, err)
	}
}

func TestSetupServicesExplainsPreviewAndKeepsCustomDirectoryOffline(t *testing.T) {
	setupRefreshHome(t)
	directory := filepath.Join(t.TempDir(), "units")
	fake := setupRefreshManager(t, "")

	stdout, _, err := executeCommand(t, "setup", "services", "--dir", directory, testNoConfigFlag)
	if err != nil || !strings.Contains(stdout, "To install, repeat this command with --apply.") || strings.Contains(stdout, "To start automatic saves:") {
		t.Fatalf("preview must explain installation: stdout=%q err=%v", stdout, err)
	}

	stdout, _, err = executeCommand(t, "setup", "services", "--apply", "--dir", directory, testNoConfigFlag)
	if err != nil || strings.Contains(stdout, "To start automatic saves:") || len(fake.commands(t)) != 0 {
		t.Fatalf("custom-directory setup must stay offline: stdout=%q err=%v commands=%v", stdout, err, fake.commands(t))
	}
}

// executeSetupServices runs the production pipeline against a fake systemctl on PATH.
func executeSetupServices(t *testing.T, arguments ...string) commandExecution {
	t.Helper()

	args := append([]string{"setup", "services", "--program", "/private-program-canary/sesh", testJSONFlag}, arguments...)

	return executeJSON(t, args...)
}

func TestSetupServicesApplyReloadsAndPreservesActiveServices(t *testing.T) {
	setupRefreshHome(t)
	seedSetupRefreshServices(t)
	configPath, logPath := runtimeConfig(t, "info")
	fake := setupRefreshManager(t, "")

	execution := executeSetupServices(t, "--apply", testConfigFlag, configPath)
	if execution.Err != nil || !execution.Envelope.OK || execution.Stderr != "" {
		t.Fatalf("refresh = %+v", execution)
	}

	result := clitest.DecodePayload[setupServicesResultJSON](t, execution.Envelope.Result)
	if !result.Applied || result.Written != 3 || result.Manager == nil || !result.Manager.Reloaded {
		t.Fatalf("refresh result = %+v", result)
	}

	if !slices.Equal(result.Manager.Restarted, []string{setup.AutosaveTimerName, setup.RemoteServiceName}) {
		t.Fatalf("restart result = %+v", result.Manager)
	}

	if !slices.Equal(fake.commands(t), []string{"daemon-reload", "try-restart " + setup.AutosaveTimerName, "try-restart " + setup.RemoteServiceName}) {
		t.Fatalf("manager commands = %v", fake.commands(t))
	}

	assertRuntimeLog(t, logPath, "cli", "refresh", "info", "ok", configPath, "private-program-canary", "systemctl")
}

func setupRefreshHome(t *testing.T) {
	t.Helper()

	if runtime.GOOS != setup.PlatformLinux {
		t.Skip("Linux service-manager refresh")
	}

	isolatedSetupHome(t)
}

func seedSetupRefreshServices(t *testing.T) string {
	t.Helper()

	directory, err := setup.ServiceDirectory(setup.PlatformLinux)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{setup.AutosaveTimerName, setup.RemoteServiceName} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("# "+setup.ManagedMarker+"\nold private definition\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return directory
}

type fakeSystemctl struct {
	callsPath string
}

// commands returns the recorded systemctl mutations, without inspections.
func (fake fakeSystemctl) commands(t *testing.T) []string {
	t.Helper()

	var commands []string

	for _, call := range fake.calls(t) {
		if !strings.HasPrefix(call, "--user show ") {
			commands = append(commands, strings.TrimPrefix(call, "--user "))
		}
	}

	return commands
}

func (fake fakeSystemctl) calls(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(fake.callsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// setupRefreshManager puts a systemctl stub first on PATH. failing names the
// operation that exits non-zero: "inspect", "daemon-reload" or "try-restart <unit>".
func setupRefreshManager(t *testing.T, failing string) fakeSystemctl {
	t.Helper()

	binDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "calls")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %[1]q
case "$*" in
"--user show --property=ActiveState --value "*)
	if [ %[2]q = inspect ]; then exit %[3]d; fi
	echo active
	exit 0;;
esac
if [ %[4]q = "$*" ]; then exit %[3]d; fi
exit 0
`, callsPath, failing, fakeSystemctlFailureExit, "--user "+failing)

	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o700); err != nil { //nolint:gosec // the stub must be executable.
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return fakeSystemctl{callsPath: callsPath}
}

func requireFakeSystemctlFailure(t *testing.T, err error) {
	t.Helper()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != fakeSystemctlFailureExit {
		t.Fatalf("error = %v, want the manager command failure", err)
	}
}

func TestSetupServicesManagerFailureRetainsPartialJSONAndOneErrorRecord(t *testing.T) {
	for _, phase := range []string{"inspect", "daemon-reload", "try-restart " + setup.RemoteServiceName} {
		t.Run(phase, func(t *testing.T) {
			setupRefreshHome(t)
			directory := seedSetupRefreshServices(t)
			configPath, logPath := runtimeConfig(t, "info")
			setupRefreshManager(t, phase)

			execution := executeSetupServices(t, "--apply", testConfigFlag, configPath)
			requireFakeSystemctlFailure(t, execution.Err)

			if execution.Envelope.OK || execution.Envelope.Error == nil || execution.Stderr != "" {
				t.Fatalf("failed refresh = %+v", execution)
			}

			result := clitest.DecodePayload[setupServicesResultJSON](t, execution.Envelope.Result)
			if result.Manager == nil {
				t.Fatal("missing partial manager result")
			}

			if phase == "inspect" {
				assertRefreshInspectionDidNotWrite(t, directory, result)
			} else if result.Written != 3 {
				t.Fatalf("lost completed definition writes: %+v", result)
			}

			assertRefreshPartialManagerResult(t, phase, result.Manager)

			if strings.Contains(execution.Stdout+execution.Stderr, "exit status") {
				t.Fatal("private manager diagnostics in JSON")
			}

			assertRuntimeLog(t, logPath, "cli", "refresh", "error", "failed", directory, configPath, "private-program-canary", "exit status")
		})
	}
}

func assertRefreshPartialManagerResult(t *testing.T, phase string, manager *setup.ServiceRefreshResult) {
	t.Helper()

	if phase == "try-restart "+setup.RemoteServiceName && (!manager.Reloaded || !slices.Equal(manager.Restarted, []string{setup.AutosaveTimerName})) {
		t.Fatalf("lost completed manager operations: %+v", manager)
	}
}

func TestSetupServicesDisabledLoggingDoesNotCreateSink(t *testing.T) {
	setupRefreshHome(t)
	seedSetupRefreshServices(t)
	configPath, logPath := runtimeConfig(t, "disabled")
	setupRefreshManager(t, "daemon-reload")

	execution := executeSetupServices(t, "--apply", testConfigFlag, configPath)
	if execution.Err == nil || execution.Envelope.OK || execution.Stderr != "" {
		t.Fatalf("refresh with logging disabled = %+v", execution)
	}

	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled logging created a sink: %v", err)
	}
}

func assertRefreshInspectionDidNotWrite(t *testing.T, directory string, result setupServicesResultJSON) {
	t.Helper()

	if result.Written != 0 {
		t.Fatalf("failed inspection wrote definitions: %+v", result)
	}

	if _, err := os.Stat(filepath.Join(directory, setup.AutosaveServiceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed inspection created autosave definition: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(directory, setup.RemoteServiceName))
	if err != nil || !strings.Contains(string(content), "old private definition") {
		t.Fatalf("failed inspection replaced monitor: %s, %v", content, err)
	}
}

func TestSetupServicesPreviewAndExplicitDirectoryNeverContactManager(t *testing.T) {
	for _, mode := range []string{"preview", "custom-directory", "explicit-default-directory"} {
		t.Run(mode, func(t *testing.T) {
			setupRefreshHome(t)
			directory := seedSetupRefreshServices(t)
			fake := setupRefreshManager(t, "")
			args := []string{testNoConfigFlag}

			if mode == "custom-directory" {
				directory = filepath.Join(t.TempDir(), "custom-units")
			}

			if mode != "preview" {
				args = append(args, "--apply", "--dir", directory)
			}

			execution := executeSetupServices(t, args...)
			if execution.Err != nil || !execution.Envelope.OK || execution.Stderr != "" {
				t.Fatalf("definition-only refresh = %+v", execution)
			}

			assertDefinitionOnlyRefresh(t, mode == "preview", directory, clitest.DecodePayload[setupServicesResultJSON](t, execution.Envelope.Result))

			if calls := fake.calls(t); len(calls) != 0 {
				t.Fatalf("contacted manager from preview or explicit directory: %v", calls)
			}
		})
	}
}

func assertDefinitionOnlyRefresh(t *testing.T, preview bool, directory string, result setupServicesResultJSON) {
	t.Helper()

	if result.Manager != nil {
		t.Fatalf("definition-only result claims manager operation: %+v", result.Manager)
	}

	if preview {
		assertRefreshInspectionDidNotWrite(t, directory, result)

		return
	}

	if !result.Applied || result.Written != 3 {
		t.Fatalf("definition-only apply = %+v", result)
	}
}

// setupStatefulManager puts a systemctl stub first on PATH that reports active for every unit
// except those in failed, and the same UnitFileState for all of them. It records every call.
func setupStatefulManager(t *testing.T, failed []string, unitFileState string) fakeSystemctl {
	t.Helper()

	binDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "calls")

	var failedCases strings.Builder
	for _, name := range failed {
		fmt.Fprintf(&failedCases, "\"--user show --property=ActiveState --value %s\") echo failed; exit 0;;\n", name)
	}

	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %[1]q
case "$*" in
%[2]s"--user show --property=ActiveState --value "*) echo active; exit 0;;
"--user show --property=UnitFileState --value "*) echo %[3]s; exit 0;;
esac
exit 0
`, callsPath, failedCases.String(), unitFileState)

	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o700); err != nil { //nolint:gosec // the stub must be executable.
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return fakeSystemctl{callsPath: callsPath}
}

func seedSetupCoordinator(t *testing.T) {
	t.Helper()

	directory := seedSetupRefreshServices(t)
	if err := os.WriteFile(filepath.Join(directory, setup.CoordinatorServiceName), []byte("# "+setup.ManagedMarker+"\nExecStart=/old/sesh daemon run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupServicesStartsAFailedServiceAgainAfterFixingIt(t *testing.T) {
	setupRefreshHome(t)
	seedSetupCoordinator(t)
	fake := setupStatefulManager(t, []string{setup.CoordinatorServiceName}, "enabled")

	stdout, stderr, err := executeCommand(t, "setup", "services", "--with-coordinator", "--apply", testNoConfigFlag)
	if err != nil || stderr != "" {
		t.Fatalf("setup failed: %v\nstderr: %s", err, stderr)
	}

	assertServiceFileLabel(t, stdout, setup.CoordinatorServiceName, "updated")

	for _, want := range []string{"Reloaded systemd.", "Started " + setup.CoordinatorServiceName + " again; it had failed."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output is missing %q:\n%s", want, stdout)
		}
	}

	commands := fake.commands(t)
	if !slices.Contains(commands, "reset-failed "+setup.CoordinatorServiceName) || !slices.Contains(commands, "restart "+setup.CoordinatorServiceName) {
		t.Fatalf("the failed coordinator was not started again: %v", commands)
	}

	for _, command := range commands {
		if strings.HasPrefix(command, "enable") || strings.Contains(command, setup.TmuxServiceName) {
			t.Fatalf("setup changed enablement or touched tmux: %v", commands)
		}
	}
}

func TestSetupServicesSaysWhenAutomaticSavesAreAlreadyOn(t *testing.T) {
	setupRefreshHome(t)
	seedSetupCoordinator(t)
	setupStatefulManager(t, nil, "enabled")

	stdout, _, err := executeCommand(t, "setup", "services", "--with-coordinator", "--apply", testNoConfigFlag)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if !strings.Contains(stdout, "Automatic saves are on: sesh-autosave.timer, sesh-coordinator.service enabled and running.") ||
		strings.Contains(stdout, "enable --now") {
		t.Fatalf("setup must report that nothing is left to start:\n%s", stdout)
	}
}

func TestSetupServicesLabelsEachFileByWhatHappened(t *testing.T) {
	setupRefreshHome(t)
	seedSetupRefreshServices(t)
	setupStatefulManager(t, nil, "disabled")

	stdout, _, err := executeCommand(t, "setup", "services", "--apply", testNoConfigFlag)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	assertServiceFileLabel(t, stdout, setup.AutosaveTimerName, "updated")
	assertServiceFileLabel(t, stdout, setup.AutosaveServiceName, "created")

	if !strings.Contains(stdout, "To start automatic saves:\n  systemctl --user enable --now sesh-autosave.timer\n") {
		t.Fatalf("output must say how to start the disabled timer:\n%s", stdout)
	}

	if strings.Contains(stdout, "[stale]") || strings.Contains(stdout, "[current]") {
		t.Fatalf("output must not use internal plan states:\n%s", stdout)
	}
}

// assertServiceFileLabel checks the outcome printed for one service file, ignoring column padding.
func assertServiceFileLabel(t *testing.T, stdout, name, label string) {
	t.Helper()

	for line := range strings.SplitSeq(stdout, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == name {
			if got := strings.Join(fields[1:], " "); got != label {
				t.Fatalf("%s is labeled %q, want %q:\n%s", name, got, label, stdout)
			}

			return
		}
	}

	t.Fatalf("no line for %s:\n%s", name, stdout)
}
