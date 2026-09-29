package cmdsetup_test

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

	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o700); err != nil { //nolint:gosec // reason: the stub must be executable.
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
